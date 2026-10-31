package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/asciimoth/ghrepo/cache"
)

// GitHub stores SHA-256-named assets in numbered GitHub Release buckets.
type GitHub struct {
	Account, Repository, Prefix, Token string
	BucketSize                         int
	APIURL                             string
	Client                             *http.Client
	Fetcher                            cache.Fetcher
	RedirectHosts                      []string
}

type release struct {
	ID        int64  `json:"id"`
	Tag       string `json:"tag_name"`
	UploadURL string `json:"upload_url"`
	Immutable bool   `json:"immutable"`
}

// Asset identifies an asset owned by this repository and release prefix.
type Asset struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"browser_download_url"`
	State      string `json:"state"`
	Size       int64  `json:"size"`
	ReleaseTag string `json:"-"`
}

type byteBody struct{ *bytes.Reader }

func (*byteBody) Close() error { return nil }

func (g *GitHub) base() string {
	if g.APIURL != "" {
		return strings.TrimRight(g.APIURL, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) httpClient() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

func (g *GitHub) validateTarget() error {
	if g.Account == "" || g.Repository == "" || g.Token == "" {
		return errors.New("GitHub target is not configured")
	}
	for _, v := range []string{g.Account, g.Repository} {
		if !assetNameRE.MatchString(v) || strings.Contains(v, "..") {
			return errors.New("invalid GitHub target")
		}
	}
	return nil
}

func (g *GitHub) validateStorage() error {
	if err := g.validateTarget(); err != nil {
		return err
	}
	if g.Prefix == "" || g.BucketSize < 1 || g.BucketSize > 1000 || !assetNameRE.MatchString(g.Prefix) || strings.Contains(g.Prefix, "..") {
		return errors.New("invalid GitHub release storage settings")
	}
	return nil
}

func (g *GitHub) api(ctx context.Context, method, path string, input io.Reader, contentType string, out any, allowed ...int) (int, error) {
	if err := g.validateTarget(); err != nil {
		return 0, err
	}
	target := g.base() + path
	if strings.HasPrefix(path, "https://") {
		target = path
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return 0, errors.New("invalid GitHub API URL")
	}
	base, err := url.Parse(g.base())
	if err != nil || (u.Host != base.Host && u.Host != "uploads.github.com") {
		return 0, errors.New("unexpected GitHub API host")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, input)
	if err != nil {
		return 0, errors.New("invalid GitHub API request")
	}
	if file, ok := input.(*os.File); ok {
		info, e := file.Stat()
		if e != nil {
			return 0, errors.New("cannot stat upload file")
		}
		req.ContentLength = info.Size()
	} else if sized, ok := input.(interface{ Len() int }); ok {
		req.ContentLength = int64(sized.Len())
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := *g.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("GitHub API request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	for _, code := range allowed {
		if resp.StatusCode == code {
			if out != nil && resp.StatusCode != http.StatusNotFound {
				if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
					return 0, errors.New("invalid GitHub API response")
				}
			}
			return code, nil
		}
	}
	return resp.StatusCode, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
}

func (g *GitHub) repoPath() string {
	return "/repos/" + url.PathEscape(g.Account) + "/" + url.PathEscape(g.Repository)
}

func (g *GitHub) releaseByTag(ctx context.Context, tag string) (release, bool, error) {
	var r release
	code, err := g.api(ctx, "GET", g.repoPath()+"/releases/tags/"+url.PathEscape(tag), nil, "", &r, 200, 404)
	if code == 404 {
		return r, false, nil
	}
	return r, err == nil, err
}

func (g *GitHub) createRelease(ctx context.Context, tag string) (release, error) {
	body, _ := json.Marshal(map[string]any{"tag_name": tag, "name": tag, "draft": false, "prerelease": false})
	var r release
	_, err := g.api(ctx, "POST", g.repoPath()+"/releases", bytes.NewReader(body), "application/json", &r, 201)
	if err != nil {
		return r, err
	}
	return r, nil
}

func (g *GitHub) releaseAssets(ctx context.Context, id int64) ([]Asset, error) {
	var all []Asset
	for page := 1; ; page++ {
		var part []Asset
		path := g.repoPath() + "/releases/" + strconv.FormatInt(id, 10) + "/assets?per_page=100&page=" + strconv.Itoa(page)
		_, err := g.api(ctx, "GET", path, nil, "", &part, 200)
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
		if len(part) < 100 {
			return all, nil
		}
		if page >= 20 {
			return nil, errors.New("too many release assets")
		}
	}
}

func (g *GitHub) download(ctx context.Context, raw string, max int64) ([]byte, error) {
	if _, _, ok := g.assetURLParts(raw); !ok {
		return nil, errors.New("invalid public release URL")
	}
	fetcher := g.Fetcher
	if fetcher == nil {
		fetcher = cache.HTTPFetcher{}
	}
	body, err := fetcher.Fetch(ctx, raw, []string{"github.com"}, g.RedirectHosts)
	if err != nil {
		return nil, errors.New("public asset download failed")
	}
	if body == nil {
		return nil, errors.New("public asset download returned no body")
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, errors.New("public asset exceeds size limit")
	}
	return data, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (g *GitHub) assetURLParts(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 6 || !strings.EqualFold(parts[0], g.Account) || !strings.EqualFold(parts[1], g.Repository) || parts[2] != "releases" || parts[3] != "download" || parts[4] == "" || parts[5] == "" {
		return "", "", false
	}
	return parts[4], parts[5], true
}

// AssetURLParts returns the release tag and name for a URL in this repository.
func (g *GitHub) AssetURLParts(raw string) (string, string, bool) { return g.assetURLParts(raw) }

func (g *GitHub) assetURLMatches(raw, tag, name string) bool {
	actualTag, actualName, ok := g.assetURLParts(raw)
	return ok && actualTag == tag && actualName == name
}

func (g *GitHub) verifyDownload(ctx context.Context, raw string, size int64, want string) error {
	if _, _, ok := g.assetURLParts(raw); !ok {
		return errors.New("invalid public release URL")
	}
	fetcher := g.Fetcher
	if fetcher == nil {
		fetcher = cache.HTTPFetcher{}
	}
	body, err := fetcher.Fetch(ctx, raw, []string{"github.com"}, g.RedirectHosts)
	if err != nil {
		return errors.New("public asset download failed")
	}
	if body == nil {
		return errors.New("public asset download returned no body")
	}
	defer func() { _ = body.Close() }()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(body, size+1))
	if err != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != want {
		return errors.New("public asset bytes do not match")
	}
	return nil
}

// VerifyOwnedAsset checks a recorded public URL before upload resume.
func (g *GitHub) VerifyOwnedAsset(ctx context.Context, raw, name string, size int64, want string) error {
	if err := g.validateStorage(); err != nil {
		return err
	}
	tag, actual, ok := g.assetURLParts(raw)
	if !ok || actual != name || (name != want+".deb" && name != want+".rpm") {
		return errors.New("invalid recorded asset URL")
	}
	suffix, ok := strings.CutPrefix(tag, g.Prefix+"-")
	n, err := strconv.Atoi(suffix)
	if !ok || err != nil || n < 1 || strconv.Itoa(n) != suffix {
		return errors.New("invalid recorded release tag")
	}
	return g.verifyDownload(ctx, raw, size, want)
}

// Put uploads one asset without replacing an existing name. It verifies the
// public download before it returns the URL.
func (g *GitHub) Put(ctx context.Context, name string, data []byte) (string, error) {
	want := digest(data)
	return g.put(ctx, name, int64(len(data)), want, func() (io.ReadCloser, error) { return &byteBody{bytes.NewReader(data)}, nil })
}

// PutFile uploads a regular file without loading it into memory.
func (g *GitHub) PutFile(ctx context.Context, name, path string, size int64, want string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return "", errors.New("upload file changed")
	}
	open := func() (io.ReadCloser, error) {
		file, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		current, e := file.Stat()
		if e != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Size() != size {
			_ = file.Close()
			return nil, errors.New("upload file changed")
		}
		return file, nil
	}
	return g.put(ctx, name, size, want, open)
}

func (g *GitHub) put(ctx context.Context, name string, size int64, want string, open func() (io.ReadCloser, error)) (string, error) {
	if err := g.validateStorage(); err != nil {
		return "", err
	}
	if !assetNameRE.MatchString(name) || strings.Contains(name, "..") || size <= 0 || size > 2<<30 || (name != want+".deb" && name != want+".rpm") || len(want) != 64 {
		return "", errors.New("invalid release asset")
	}
	if _, err := hex.DecodeString(want); err != nil || strings.ToLower(want) != want {
		return "", errors.New("invalid release asset hash")
	}
	var chosen release
	chosenTag := ""
	for bucket := 1; bucket <= 100000; bucket++ {
		tag := g.Prefix + "-" + strconv.Itoa(bucket)
		r, found, err := g.releaseByTag(ctx, tag)
		if err != nil {
			return "", err
		}
		created := !found
		if !found {
			if chosen.ID != 0 {
				break
			}
			r, err = g.createRelease(ctx, tag)
			if err != nil {
				// A concurrent writer may have created the bucket.
				r, found, err = g.releaseByTag(ctx, tag)
				if err != nil || !found {
					return "", errors.New("release creation failed")
				}
				created = false
			}
		}
		assets, err := g.releaseAssets(ctx, r.ID)
		if err != nil {
			return "", err
		}
		assetCount := len(assets)
		for _, a := range assets {
			if a.Name != name {
				continue
			}
			if a.State == "starter" && a.Size == 0 {
				if r.Immutable {
					return "", errors.New("immutable release contains an incomplete asset")
				}
				path := g.repoPath() + "/releases/assets/" + strconv.FormatInt(a.ID, 10)
				if _, err := g.api(ctx, "DELETE", path, nil, "", nil, 204); err != nil {
					return "", err
				}
				assetCount--
				continue
			}
			if !g.assetURLMatches(a.URL, tag, name) {
				return "", errors.New("existing asset has unexpected URL")
			}
			if a.Size != size || g.verifyDownload(ctx, a.URL, size, want) != nil {
				return "", errors.New("existing asset has different or unavailable bytes")
			}
			return a.URL, nil
		}
		if r.Immutable {
			if created {
				return "", errors.New("immutable GitHub Releases cannot accept uploads; use mutable releases for this backend")
			}
			continue
		}
		if assetCount < g.BucketSize && chosen.ID == 0 {
			chosen = r
			chosenTag = tag
		}
		if created {
			break
		}
	}
	if chosen.ID == 0 {
		return "", errors.New("release bucket limit reached")
	}
	return g.uploadAsset(ctx, chosen, chosenTag, name, size, want, open)
}

func (g *GitHub) uploadAsset(ctx context.Context, r release, tag, name string, size int64, want string, open func() (io.ReadCloser, error)) (string, error) {
	uploadURL := strings.Split(r.UploadURL, "{")[0]
	u, e := url.Parse(uploadURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !g.uploadPathMatches(u.Path, r.ID) {
		return "", errors.New("invalid release upload URL")
	}
	// GitHub uses uploads.github.com. A custom API host supports local TLS tests.
	if u.Host != "uploads.github.com" && u.Host != mustHost(g.base()) {
		return "", errors.New("unexpected release upload host")
	}
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()
	body, err := open()
	if err != nil {
		return "", err
	}
	var asset Asset
	_, err = g.api(ctx, "POST", u.String(), body, "application/octet-stream", &asset, 201)
	_ = body.Close()
	if err != nil {
		return "", err
	}
	if asset.Name != name || asset.State != "uploaded" || asset.Size != size {
		return "", errors.New("invalid uploaded asset response")
	}
	if !g.assetURLMatches(asset.URL, tag, name) {
		return "", errors.New("uploaded asset has unexpected URL")
	}
	if err := g.verifyDownload(ctx, asset.URL, size, want); err != nil {
		return "", errors.New("uploaded asset public bytes do not match")
	}
	return asset.URL, nil
}

func mustHost(raw string) string { u, _ := url.Parse(raw); return u.Host }

func (g *GitHub) uploadPathMatches(path string, id int64) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 6 && parts[0] == "repos" && strings.EqualFold(parts[1], g.Account) && strings.EqualFold(parts[2], g.Repository) && parts[3] == "releases" && parts[4] == strconv.FormatInt(id, 10) && parts[5] == "assets"
}

// Get reads an asset from the owned releases.
func (g *GitHub) Get(ctx context.Context, name string) ([]byte, error) {
	assets, err := g.ListOwnedAssets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.Name == name {
			data, err := g.download(ctx, a.URL, 2<<30)
			if err != nil {
				return nil, err
			}
			if int64(len(data)) != a.Size || digest(data) != name[:64] {
				return nil, errors.New("owned asset bytes do not match its name")
			}
			return data, nil
		}
	}
	return nil, errors.New("asset not found")
}

// ListOwnedAssets lists only assets in releases with this configured prefix.
func (g *GitHub) ListOwnedAssets(ctx context.Context) ([]Asset, error) {
	if err := g.validateStorage(); err != nil {
		return nil, err
	}
	var result []Asset
	for page := 1; page <= 100; page++ {
		var releases []release
		_, err := g.api(ctx, "GET", g.repoPath()+"/releases?per_page=100&page="+strconv.Itoa(page), nil, "", &releases, 200)
		if err != nil {
			return nil, err
		}
		for _, r := range releases {
			suffix, ok := strings.CutPrefix(r.Tag, g.Prefix+"-")
			n, e := strconv.Atoi(suffix)
			if !ok || e != nil || n < 1 || strconv.Itoa(n) != suffix {
				continue
			}
			assets, e := g.releaseAssets(ctx, r.ID)
			if e != nil {
				return nil, e
			}
			for _, a := range assets {
				if !g.ownedAsset(r.Tag, a) {
					continue
				}
				a.ReleaseTag = r.Tag
				result = append(result, a)
			}
		}
		if len(releases) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("too many releases")
}

func (g *GitHub) ownedAsset(tag string, a Asset) bool {
	parts := strings.Split(a.Name, ".")
	if len(parts) != 2 || (parts[1] != "deb" && parts[1] != "rpm") || len(parts[0]) != 64 {
		return false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil || strings.ToLower(parts[0]) != parts[0] {
		return false
	}
	return g.assetURLMatches(a.URL, tag, a.Name)
}

// CreateRepository creates a public repository for the configured account.
func (g *GitHub) CreateRepository(ctx context.Context) error {
	if err := g.validateTarget(); err != nil {
		return err
	}
	var existing struct {
		Private bool `json:"private"`
	}
	code, err := g.api(ctx, "GET", g.repoPath(), nil, "", &existing, 200, 404)
	if err != nil {
		return err
	}
	if code == 200 {
		if existing.Private {
			return errors.New("existing GitHub repository is private")
		}
		return nil
	}
	var user struct {
		Login string `json:"login"`
	}
	_, err = g.api(ctx, "GET", "/user", nil, "", &user, 200)
	if err != nil {
		return err
	}
	path := "/user/repos"
	if !strings.EqualFold(user.Login, g.Account) {
		path = "/orgs/" + url.PathEscape(g.Account) + "/repos"
	}
	body, _ := json.Marshal(map[string]any{"name": g.Repository, "private": false, "auto_init": false})
	_, err = g.api(ctx, "POST", path, bytes.NewReader(body), "application/json", nil, 201)
	return err
}

// ConfigurePages selects GitHub Actions as the Pages build source.
func (g *GitHub) ConfigurePages(ctx context.Context) error {
	if err := g.validateTarget(); err != nil {
		return err
	}
	var current struct {
		BuildType string `json:"build_type"`
	}
	code, err := g.api(ctx, "GET", g.repoPath()+"/pages", nil, "", &current, 200, 404)
	if err != nil {
		return err
	}
	if code == 200 && current.BuildType == "workflow" {
		return nil
	}
	body := bytes.NewBufferString(`{"build_type":"workflow"}`)
	if code == 404 {
		_, err = g.api(ctx, "POST", g.repoPath()+"/pages", body, "application/json", nil, 201)
	} else {
		_, err = g.api(ctx, "PUT", g.repoPath()+"/pages", body, "application/json", nil, 204)
	}
	return err
}
