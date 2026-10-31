package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
)

type releaseSource struct {
	owner, repo, tag, asset string
}

var releaseTagRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

func parseReleaseSource(raw string) (releaseSource, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return releaseSource{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" ||
		!githubRepoName.MatchString(parts[0]) || !githubRepoName.MatchString(parts[1]) ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." ||
		!releaseTagRE.MatchString(parts[4]) || parts[5] == "" || strings.ContainsAny(parts[5], "\\%?#") {
		return releaseSource{}, false
	}
	return releaseSource{parts[0], parts[1], parts[4], parts[5]}, true
}

type latestRelease struct {
	Tag    string        `json:"tag_name"`
	Assets []latestAsset `json:"assets"`
}

type latestAsset struct {
	Name  string `json:"name"`
	URL   string `json:"browser_download_url"`
	State string `json:"state"`
}

func (c Runner) latestGitHubRelease(ctx context.Context, source releaseSource) (latestRelease, error) {
	base := c.GitHubAPIURL
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(source.owner) + "/" + url.PathEscape(source.repo) + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return latestRelease{}, errors.New("invalid GitHub API request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{}
	if c.GitHubClient != nil {
		client = *c.GitHubClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("GitHub API redirect is not allowed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return latestRelease{}, errors.New("GitHub release lookup failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return latestRelease{}, fmt.Errorf("GitHub release lookup returned HTTP %d", resp.StatusCode)
	}
	var release latestRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&release); err != nil || !releaseTagRE.MatchString(release.Tag) {
		return latestRelease{}, errors.New("invalid GitHub release response")
	}
	return release, nil
}

func releaseAsset(source releaseSource, release latestRelease, oldVersion string) (string, bool) {
	version := strings.TrimPrefix(release.Tag, "v")
	withTag := strings.ReplaceAll(source.asset, source.tag, release.Tag)
	withVersion := strings.ReplaceAll(source.asset, oldVersion, version)
	if at := strings.Index(source.asset, source.tag); at >= 0 {
		withVersion = strings.ReplaceAll(source.asset[:at], oldVersion, version) + source.tag +
			strings.ReplaceAll(source.asset[at+len(source.tag):], oldVersion, version)
	}
	names := []string{
		strings.ReplaceAll(withTag, oldVersion, version),
		withTag,
		withVersion,
		source.asset,
	}
	for _, name := range names {
		want := "https://github.com/" + source.owner + "/" + source.repo + "/releases/download/" + release.Tag + "/" + name
		for _, asset := range release.Assets {
			if asset.Name == name && asset.URL == want && asset.State == "uploaded" {
				return want, true
			}
		}
	}
	return "", false
}

func (c Runner) update(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("dry-run", false, "report latest releases without changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: ghrepo update [--dry-run]")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	out := c.Out
	if out == nil {
		out = io.Discard
	}
	var pending bytes.Buffer
	report := out
	if !*dry {
		report = &pending
	}
	latest := map[string]latestRelease{}
	_, _, cacheRoot := c.paths()
	cacheDir := filepath.Join(cacheRoot, "sha256")
	if *dry {
		cacheDir, err = os.MkdirTemp("", "ghrepo-update-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(cacheDir) }()
	}
	changed := false
	fetcher := c.Fetcher
	if fetcher == nil {
		fetcher = cache.HTTPFetcher{}
	}
	for _, old := range append([]contract.Package(nil), m.Packages...) {
		if old.State != "active" || old.Source != "upstream" {
			continue
		}
		source, ok := parseReleaseSource(old.URL)
		if !ok {
			_, _ = fmt.Fprintf(report, "skip %s: unsupported source URL\n", old.Path)
			continue
		}
		key := source.owner + "/" + source.repo
		release, found := latest[key]
		if !found {
			release, err = c.latestGitHubRelease(ctx, source)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			latest[key] = release
		}
		if source.tag == release.Tag {
			_, _ = fmt.Fprintf(report, "%s: current %s, latest %s (release %s, up to date)\n", old.Path, old.Identity.Version, old.Identity.Version, release.Tag)
			continue
		}
		assetURL, ok := releaseAsset(source, release, old.Identity.Version)
		if !ok {
			_, _ = fmt.Fprintf(report, "%s: current %s, latest release %s (matching asset not found)\n", old.Path, old.Identity.Version, release.Tag)
			continue
		}
		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			return err
		}
		before := len(m.Packages)
		check := func(candidate contract.Package) error {
			if candidate.Format != old.Format || candidate.Identity.Name != old.Identity.Name ||
				candidate.Identity.Architecture != old.Identity.Architecture {
				return errors.New("latest release asset has a different package identity")
			}
			return nil
		}
		if err := addOneChecked(ctx, assetURL, cacheDir, r, &m, fetcher, -1, c.now(), check); err != nil {
			return fmt.Errorf("%s: %w", old.Path, err)
		}
		if len(m.Packages) == before {
			_, _ = fmt.Fprintf(report, "%s: latest release %s already added\n", old.Path, release.Tag)
			continue
		}
		added := m.Packages[before]
		if *dry {
			_, _ = fmt.Fprintf(report, "%s: current %s, latest %s (release %s): %s\n", old.Path, old.Identity.Version, added.Identity.Version, release.Tag, assetURL)
			continue
		}
		changed = true
		_, _ = fmt.Fprintf(report, "%s: added %s from release %s\n", old.Path, added.Identity.Version, release.Tag)
	}
	if *dry {
		return nil
	}
	if changed {
		if err := c.save(m, r); err != nil {
			return err
		}
	}
	_, err = io.Copy(out, &pending)
	return err
}
