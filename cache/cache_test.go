package cache

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
)

type failingFetcher struct{ err error }

func (f failingFetcher) Fetch(context.Context, string, []string, []string) (io.ReadCloser, error) {
	return nil, f.err
}

func testConfig() contract.Repository {
	return contract.Repository{
		Schema:     1,
		Repository: contract.Repo{URL: "https://github.com/example/repo", Branch: "main", Layout: "flat-v1"},
		APT:        contract.APT{Suite: "stable", Component: "main", Architectures: []string{"amd64"}},
		RPM:        contract.RPM{ID: "test", Name: "Test", Architectures: []string{"x86_64"}, PackageSignature: "metadata-only"},
		Downloads:  contract.Download{SourceHosts: []string{"source.test"}, RedirectHosts: []string{"redirect.test"}, MaxBytes: 100},
		Site:       contract.Site{BaseURL: "https://example.com/repo", MaxBytes: 1000, RetentionDays: 30},
	}
}

func testEntry(raw string) contract.Package {
	data := []byte("package-data")
	sum := sha256.Sum256(data)
	p := contract.Package{URL: raw, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Source: "upstream", State: "active", Format: "deb", Identity: contract.Identity{Name: "hello", Version: "1", Architecture: "amd64"}}
	p.Path, _ = contract.ServedPath(p)
	return p
}

func TestTLSFetchAndCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		switch req.URL.Path {
		case "/ok":
			_, _ = fmt.Fprint(w, "package-data")
		case "/redirect":
			http.Redirect(w, req, "https://redirect.test/ok", http.StatusFound)
		case "/bad-redirect":
			http.Redirect(w, req, "https://other.test/ok", http.StatusFound)
		case "/http-redirect":
			http.Redirect(w, req, "http://redirect.test/ok", http.StatusFound)
		case "/error":
			http.Error(w, "no", http.StatusNotFound)
		case "/large":
			_, _ = fmt.Fprint(w, strings.Repeat("a", 101))
		case "/changed":
			_, _ = fmt.Fprint(w, "other-bytes!")
		case "/cut":
			w.Header().Set("Content-Length", "100")
			_, _ = fmt.Fprint(w, "short")
		}
	}))
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	fetcher := HTTPFetcher{Client: &http.Client{Transport: transport}}
	r := testConfig()
	root := t.TempDir()
	p := testEntry("https://source.test/redirect")
	name, err := Ensure(context.Background(), root, r, p, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(name, p); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := Ensure(context.Background(), root, r, p, fetcher); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("cache hit made network request")
	}
	changedSource := p
	changedSource.URL = "https://source.test/changed"
	if _, err := FetchVerified(context.Background(), root, r, changedSource, fetcher); err == nil {
		t.Fatal("forced fetch accepted changed source bytes")
	}
	if err := VerifyFile(name, p); err != nil {
		t.Fatal("failed forced fetch changed the old cache entry")
	}
	if _, err := FetchVerified(context.Background(), root, r, p, fetcher); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), root, r, p, fetcher); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(name, p); err != nil {
		t.Fatal("corrupt cache was not replaced")
	}
}

func TestFetchFailures(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/redirect":
			http.Redirect(w, req, "https://other.test/ok", http.StatusFound)
		case "/private":
			http.Redirect(w, req, "https://127.0.0.1/ok", http.StatusFound)
		case "/http":
			http.Redirect(w, req, "http://redirect.test/ok", http.StatusFound)
		case "/error":
			http.Error(w, "bad", http.StatusInternalServerError)
		case "/large":
			_, _ = fmt.Fprint(w, strings.Repeat("a", 101))
		case "/changed":
			_, _ = fmt.Fprint(w, "other-bytes!")
		case "/cut":
			w.Header().Set("Content-Length", "100")
			_, _ = fmt.Fprint(w, "short")
		}
	}))
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	fetcher := HTTPFetcher{Client: &http.Client{Transport: transport}}
	r := testConfig()
	for _, path := range []string{"redirect", "private", "http", "error", "large", "changed", "cut"} {
		t.Run(path, func(t *testing.T) {
			p := testEntry("https://source.test/" + path)
			root := t.TempDir()
			if _, err := Ensure(context.Background(), root, r, p, fetcher); err == nil {
				t.Fatal("accepted failed download")
			}
			if _, err := os.Stat(root + "/sha256/" + p.SHA256); !os.IsNotExist(err) {
				t.Fatalf("failed download left cache file: %v", err)
			}
		})
	}
}

func TestPrivateAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "::1", "fe80::1"} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatalf("accepted private address %s", raw)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("rejected public address")
	}
	if _, err := dialPublic(context.Background(), "tcp", "localhost:443"); err == nil {
		t.Fatal("connected to local address")
	}
}

func TestFetcherErrorRedaction(t *testing.T) {
	secret := "private-credential"
	_, err := Ensure(context.Background(), t.TempDir(), testConfig(), testEntry("https://source.test/file"), failingFetcher{err: errors.New(secret)})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("fetch error exposed credential: %v", err)
	}
	_, err = Ensure(context.Background(), t.TempDir(), testConfig(), testEntry("https://source.test/file"), failingFetcher{})
	if err == nil {
		t.Fatal("accepted nil response body")
	}
}
