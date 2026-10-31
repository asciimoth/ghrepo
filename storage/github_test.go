package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/cache"
)

type assetFetcher struct{ data map[string][]byte }

func (f assetFetcher) Fetch(_ context.Context, raw string, _, _ []string) (io.ReadCloser, error) {
	data, ok := f.data[raw]
	if !ok {
		return nil, fmt.Errorf("missing test asset")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

var _ cache.Fetcher = assetFetcher{}

func TestGitHubReleaseUploadAndPages(t *testing.T) {
	ctx := context.Background()
	assets := map[string][]Asset{}
	contents := map[string][]byte{}
	rejectUpload := false
	nextID := int64(1)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("missing authorization")
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-1"):
			if _, ok := assets["pkg-1"]; !ok {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(release{ID: 1, Tag: "pkg-1", UploadURL: server.URL + "/repos/acct/repo/releases/1/assets{?name,label}"})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-2"):
			if _, ok := assets["pkg-2"]; !ok {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(release{ID: 2, Tag: "pkg-2", UploadURL: server.URL + "/repos/acct/repo/releases/2/assets{?name,label}"})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-3"):
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/repos/acct/repo/releases":
			var body struct {
				Tag string `json:"tag_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := assets[body.Tag]; ok {
				t.Errorf("created existing release")
			}
			assets[body.Tag] = []Asset{}
			id := int64(1)
			if body.Tag == "pkg-2" {
				id = 2
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(release{ID: id, Tag: body.Tag, UploadURL: fmt.Sprintf("%s/repos/acct/repo/releases/%d/assets{?name,label}", server.URL, id)})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/") && strings.HasSuffix(r.URL.Path, "/assets"):
			tag := "pkg-1"
			if strings.Contains(r.URL.Path, "/2/") {
				tag = "pkg-2"
			}
			_ = json.NewEncoder(w).Encode(assets[tag])
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/assets"):
			if r.ContentLength <= 0 {
				t.Error("missing upload content length")
			}
			if rejectUpload {
				name := r.URL.Query().Get("name")
				assets["pkg-1"] = append(assets["pkg-1"], Asset{ID: nextID, Name: name, State: "starter", Size: 0})
				nextID++
				w.WriteHeader(502)
				return
			}
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("wrong content type")
			}
			name := r.URL.Query().Get("name")
			tag := "pkg-1"
			if strings.Contains(r.URL.Path, "/2/") {
				tag = "pkg-2"
			}
			data, _ := io.ReadAll(r.Body)
			public := "https://github.com/Acct/Repo/releases/download/" + tag + "/" + name
			a := Asset{ID: nextID, Name: name, URL: public, State: "uploaded", Size: int64(len(data))}
			nextID++
			assets[tag] = append(assets[tag], a)
			contents[public] = data
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(a)
		case r.Method == "GET" && r.URL.Path == "/repos/acct/repo/releases":
			out := []release{}
			for _, tag := range []string{"pkg-1", "pkg-2"} {
				if _, ok := assets[tag]; ok {
					id := int64(1)
					if tag == "pkg-2" {
						id = 2
					}
					out = append(out, release{ID: id, Tag: tag})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/acct/repo/releases/assets/"):
			id := strings.TrimPrefix(r.URL.Path, "/repos/acct/repo/releases/assets/")
			var keep []Asset
			for _, a := range assets["pkg-1"] {
				if fmt.Sprint(a.ID) != id {
					keep = append(keep, a)
				}
			}
			assets["pkg-1"] = keep
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/repos/acct/repo/pages":
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/repos/acct/repo/pages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["build_type"] != "workflow" {
				t.Errorf("wrong Pages build type")
			}
			w.WriteHeader(201)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	g := &GitHub{Account: "acct", Repository: "repo", Prefix: "pkg", BucketSize: 1, Token: "test-secret", APIURL: server.URL, Client: server.Client(), Fetcher: assetFetcher{contents}}
	firstName := digest([]byte("first")) + ".deb"
	secondName := digest([]byte("second")) + ".rpm"
	first, err := g.Put(ctx, firstName, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := g.Put(ctx, firstName, []byte("first"))
	if err != nil || again != first {
		t.Fatalf("repeat upload: %v", err)
	}
	if _, err := g.Put(ctx, firstName, []byte("changed")); err == nil {
		t.Fatal("replaced asset")
	}
	second, err := g.Put(ctx, secondName, []byte("second"))
	if err != nil || !strings.Contains(second, "/pkg-2/") {
		t.Fatalf("bucket rollover: %v %s", err, second)
	}
	listed, err := g.ListOwnedAssets(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("asset listing: %v %d", err, len(listed))
	}
	firstBucket := assets["pkg-1"]
	assets["pkg-1"] = nil
	againSecond, err := g.Put(ctx, secondName, []byte("second"))
	if err != nil || againSecond != second || len(assets["pkg-1"]) != 0 {
		t.Fatalf("duplicate upload into earlier bucket: %v", err)
	}
	assets["pkg-1"] = firstBucket
	contents[first] = []byte("corrupt")
	if _, err := g.Get(ctx, firstName); err == nil {
		t.Fatal("Get accepted changed public bytes")
	}
	if _, err := g.Put(ctx, firstName, []byte("first")); err == nil {
		t.Fatal("accepted changed public bytes")
	}
	g.BucketSize = 10
	rejectUpload = true
	if _, err := g.Put(ctx, digest([]byte("third"))+".deb", []byte("third")); err == nil {
		t.Fatal("accepted failed upload")
	}
	rejectUpload = false
	if _, err := g.Put(ctx, digest([]byte("third"))+".deb", []byte("third")); err != nil {
		t.Fatalf("failed to resume starter asset: %v", err)
	}
	if err := g.ConfigurePages(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubErrorsDoNotExposeToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("test-secret"))
	}))
	defer server.Close()
	g := &GitHub{Account: "acct", Repository: "repo", Prefix: "pkg", BucketSize: 1, Token: "test-secret", APIURL: server.URL, Client: server.Client()}
	_, err := g.Put(context.Background(), digest([]byte("data"))+".deb", []byte("data"))
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestGitHubRejectsNewImmutableBucket(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-1"):
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1,"tag_name":"pkg-1","immutable":true,"upload_url":%q}`, server.URL+"/repos/acct/repo/releases/1/assets{?name,label}")
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/1/assets"):
			_, _ = io.WriteString(w, "[]")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	g := &GitHub{Account: "acct", Repository: "repo", Prefix: "pkg", BucketSize: 1, Token: "secret", APIURL: server.URL, Client: server.Client()}
	_, err := g.Put(context.Background(), digest([]byte("data"))+".deb", []byte("data"))
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestGitHubRejectsForeignUploadURL(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-1"):
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":1,"tag_name":"pkg-1","upload_url":"https://uploads.github.com/repos/other/repo/releases/1/assets{?name,label}"}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/1/assets"):
			_, _ = io.WriteString(w, "[]")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	g := &GitHub{Account: "acct", Repository: "repo", Prefix: "pkg", BucketSize: 1, Token: "secret", APIURL: server.URL, Client: server.Client()}
	_, err := g.Put(context.Background(), digest([]byte("data"))+".deb", []byte("data"))
	if err == nil || !strings.Contains(err.Error(), "upload URL") {
		t.Fatalf("unexpected result: %v", err)
	}
}
