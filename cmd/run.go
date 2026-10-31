// Package cmd implements local ghrepo commands.
package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	"github.com/asciimoth/ghrepo/site"
)

type Runner struct {
	Out          io.Writer
	Dir          string
	Fetcher      cache.Fetcher
	Now          func() time.Time
	GitHubAPIURL string
	GitHubClient *http.Client
}

func (c Runner) root() string {
	if c.Dir == "" {
		return "."
	}
	return c.Dir
}
func (c Runner) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c Runner) paths() (string, string, string) {
	d := c.root()
	return filepath.Join(d, "ghrepo.yaml"), filepath.Join(d, "packages.yaml"), filepath.Join(d, "local-cache")
}
func (c Runner) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ghrepo init|add|replace|sync|source set|remove|prune|rebuild|check|publish|gc|site-build")
	}
	switch args[0] {
	case "init":
		return c.init(ctx, args[1:])
	case "add":
		return c.add(ctx, args[1:])
	case "replace":
		return c.replacePackage(ctx, args[1:])
	case "sync":
		return c.sync(ctx, args[1:])
	case "source":
		if len(args) > 1 && args[1] == "set" {
			return c.sourceSet(ctx, args[2:])
		}
		return errors.New("usage: ghrepo source set SELECTOR URL")
	case "remove":
		return c.remove(args[1:])
	case "prune":
		return c.prune(args[1:])
	case "rebuild":
		if len(args) != 1 {
			return errors.New("usage: ghrepo rebuild")
		}
		return c.rebuild(ctx)
	case "check":
		if len(args) != 1 {
			return errors.New("usage: ghrepo check")
		}
		return c.check(ctx)
	case "publish":
		return c.publish(ctx, args[1:])
	case "gc":
		return c.gc(ctx, args[1:])
	case "site-build":
		if len(args) != 1 {
			return errors.New("usage: ghrepo site-build")
		}
		return site.Build(ctx, c.root(), filepath.Join(c.root(), "build", "site"), c.Fetcher)
	default:
		return errors.New("unknown command")
	}
}
func (c Runner) load() (contract.Repository, contract.Manifest, error) {
	rp, mp, _ := c.paths()
	r, err := contract.ReadRepository(rp)
	if err != nil {
		return r, contract.Manifest{}, err
	}
	m, err := contract.ReadManifest(mp, r)
	return r, m, err
}
func (c Runner) save(m contract.Manifest, r contract.Repository) error {
	_, mp, _ := c.paths()
	return contract.WriteManifest(mp, m, r)
}

var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func initURLs(raw string) (string, string, error) {
	repoURL := raw
	if !strings.Contains(raw, "://") {
		parts := strings.Split(raw, "/")
		if len(parts) != 2 || !githubRepoName.MatchString(parts[0]) || !githubRepoName.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return "", "", errors.New("--repo must be user/repo or a GitHub HTTPS URL")
		}
		repoURL = "https://github.com/" + raw
	}
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return repoURL, "", nil
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || !githubRepoName.MatchString(parts[0]) || !githubRepoName.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return repoURL, "", nil
	}
	siteURL := "https://" + strings.ToLower(parts[0]) + ".github.io"
	if !strings.EqualFold(parts[1], parts[0]+".github.io") {
		siteURL += "/" + parts[1]
	}
	return repoURL, siteURL, nil
}

func (c Runner) init(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", "", "GitHub repository as user/repo or HTTPS URL")
	siteURL := fs.String("site", "", "site HTTPS base URL (updates existing repositories)")
	branch := fs.String("branch", "main", "")
	sourceHosts := fs.String("source-hosts", "github.com", "")
	redirectHosts := fs.String("redirect-hosts", "release-assets.githubusercontent.com,objects.githubusercontent.com", "")
	keyID := fs.String("key-id", "", "")
	createRemote := fs.Bool("create-remote", false, "create public GitHub repository and origin remote")
	configurePages := fs.Bool("configure-pages", false, "use GitHub Actions for Pages")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0])
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: ghrepo init [DIR] --repo USER/REPO [--site URL]")
	}
	siteSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "site" {
			siteSet = true
		}
	})
	dir := c.root()
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	rp := filepath.Join(dir, "ghrepo.yaml")
	var r contract.Repository
	existingRepository := false
	var originalSite contract.Site
	if _, err := os.Lstat(rp); err == nil {
		existingRepository = true
		r, err = contract.ReadRepository(rp)
		if err != nil {
			return err
		}
		originalSite = r.Site
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if *repo == "" {
			return errors.New("init requires --repo")
		}
		repoURL, defaultSite, err := initURLs(*repo)
		if err != nil {
			return err
		}
		if !siteSet {
			if defaultSite == "" {
				return errors.New("cannot derive GitHub Pages URL from --repo; set --site")
			}
			*siteURL = defaultSite
		}
		r = contract.Repository{Schema: 1, Repository: contract.Repo{URL: repoURL, Branch: *branch, Layout: "flat-v1"}, APT: contract.APT{Suite: "stable", Component: "main", Architectures: []string{"amd64", "arm64"}}, RPM: contract.RPM{ID: "ghrepo", Name: "Packages", Architectures: []string{"x86_64", "aarch64"}, PackageSignature: "metadata-only"}, Downloads: contract.Download{SourceHosts: splitList(*sourceHosts), RedirectHosts: splitList(*redirectHosts), MaxBytes: 2147483647}, Site: contract.Site{BaseURL: *siteURL, MaxBytes: 1073741824, RetentionDays: 30}, Storage: contract.Storage{Backend: "none"}}
	}
	if siteSet {
		r.Site.BaseURL = *siteURL
		u, err := url.Parse(r.Site.BaseURL)
		if err != nil {
			return errors.New("invalid site base URL")
		}
		host := strings.ToLower(u.Hostname())
		r.Site.CNAME = ""
		if !strings.HasSuffix(host, ".github.io") {
			r.Site.CNAME = host
		}
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		command := exec.Command("git", "init", "-b", r.Repository.Branch, dir)
		if output, e := command.CombinedOutput(); e != nil {
			_ = output
			return errors.New("git initialization failed")
		}
	} else if err != nil {
		return err
	}
	mp := filepath.Join(dir, "packages.yaml")
	if err := createIfAbsent(rp, func() ([]byte, error) { return contract.MarshalRepository(r) }, 0644); err != nil {
		return err
	}
	if !existingRepository {
		existing, err := contract.ReadRepository(rp)
		if err != nil {
			return err
		}
		r = existing
	}
	if err := createIfAbsent(mp, func() ([]byte, error) {
		return contract.MarshalManifest(contract.Manifest{Schema: 1, Packages: []contract.Package{}}, r)
	}, 0644); err != nil {
		return err
	}
	local := contract.Local{Schema: 1}
	if *keyID != "" {
		local.GPG.KeyID = *keyID
	}
	if err := createIfAbsent(filepath.Join(dir, ".ghrepo.local.yaml"), func() ([]byte, error) { return contract.MarshalLocal(local) }, 0600); err != nil {
		return err
	}
	if err := appendIgnore(filepath.Join(dir, ".gitignore")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0755); err != nil {
		return err
	}
	for _, item := range site.DefaultTemplates {
		if err := createIfAbsent(filepath.Join(dir, "templates", item.Name), func() ([]byte, error) {
			return []byte(item.Text), nil
		}, 0644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0755); err != nil {
		return err
	}
	if err := createIfAbsent(filepath.Join(dir, ".github", "workflows", "pages.yml"), func() ([]byte, error) {
		return []byte(workflowFor(r)), nil
	}, 0644); err != nil {
		return err
	}
	configured, err := contract.LoadEffectiveLocal(filepath.Join(dir, ".ghrepo.local.yaml"), processEnv())
	if err != nil {
		return err
	}
	if configured.GPG.KeyID != "" {
		keyPath := filepath.Join(dir, "KEY.gpg")
		if _, err := os.Lstat(keyPath); errors.Is(err, os.ErrNotExist) {
			args := []string{"--batch"}
			if configured.GPG.Keyring != "" {
				args = append(args, "--no-default-keyring", "--keyring", configured.GPG.Keyring)
			}
			args = append(args, "--export", configured.GPG.KeyID)
			command := exec.Command("gpg", args...)
			if configured.GPG.Home != "" {
				command.Env = append(os.Environ(), "GNUPGHOME="+configured.GPG.Home)
			}
			data, e := command.Output()
			if e != nil {
				return errors.New("public-key export failed")
			}
			if len(data) == 0 {
				return errors.New("public-key export is empty")
			}
			if err := contract.WriteAtomic(keyPath, data, 0644); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if *createRemote || *configurePages {
		g, err := c.githubFor(r, configured)
		if err != nil {
			return err
		}
		if *createRemote {
			origin := exec.Command("git", "remote", "get-url", "origin")
			origin.Dir = dir
			current, e := origin.Output()
			if e == nil && strings.TrimSpace(string(current)) != r.Repository.URL {
				return errors.New("existing origin does not match repository URL")
			}
			if e != nil {
				var exit *exec.ExitError
				if !errors.As(e, &exit) {
					return errors.New("cannot inspect git origin")
				}
			}
			if err := g.CreateRepository(ctx); err != nil {
				return err
			}
			if e != nil {
				command := exec.Command("git", "remote", "add", "origin", r.Repository.URL)
				command.Dir = dir
				if err := command.Run(); err != nil {
					return errors.New("git origin setup failed")
				}
			}
		}
		if *configurePages {
			if err := g.ConfigurePages(ctx); err != nil {
				return err
			}
		}
	}
	if existingRepository && siteSet && r.Site != originalSite {
		return contract.WriteRepository(rp, r)
	}
	return nil
}
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
func createIfAbsent(name string, makeData func() ([]byte, error), mode os.FileMode) error {
	if _, err := os.Lstat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := makeData()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".ghrepo-init-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), name); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func appendIgnore(name string) error {
	existing, err := os.ReadFile(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(existing)
	for _, entry := range []string{".ghrepo.local.yaml", ".env", "local-cache/", "build/", ".ghrepo-transaction/", ".ghrepo-upload/"} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			if line == entry {
				found = true
				break
			}
		}
		if !found {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			text += entry + "\n"
		}
	}
	return contract.WriteAtomic(name, []byte(text), 0644)
}
func (c Runner) add(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("add requires URLs")
	}
	if args[0] == "--upload" || args[0] == "--sign-rpm" {
		return c.addUpload(ctx, args)
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	_, _, cacheRoot := c.paths()
	dir := filepath.Join(cacheRoot, "sha256")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	fetcher := c.Fetcher
	if fetcher == nil {
		fetcher = cache.HTTPFetcher{}
	}
	for _, raw := range args {
		if err := addOne(ctx, raw, dir, r, &m, fetcher, -1, time.Time{}); err != nil {
			return err
		}
	}
	return c.save(m, r)
}

func addOne(ctx context.Context, raw, dir string, r contract.Repository, m *contract.Manifest, fetcher cache.Fetcher, replaceIndex int, now time.Time) error {
	if err := contract.ValidateSourceURL(raw, r.Downloads.SourceHosts); err != nil {
		return err
	}
	body, err := fetcher.Fetch(ctx, raw, r.Downloads.SourceHosts, r.Downloads.RedirectHosts)
	if err != nil {
		return errors.New("source download failed")
	}
	f, err := os.CreateTemp(dir, ".add-*")
	if err != nil {
		_ = body.Close()
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(body, r.Downloads.MaxBytes+1))
	closeErr := body.Close()
	if copyErr != nil || closeErr != nil || n == 0 || n > r.Downloads.MaxBytes {
		_ = f.Close()
		return errors.New("invalid or incomplete package download")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	meta, size, digest, err := inspect.File(name, r.Downloads.MaxBytes)
	if err != nil {
		return err
	}
	if size != n || digest != hex.EncodeToString(hash.Sum(nil)) {
		return errors.New("downloaded package changed")
	}
	p := contract.Package{URL: raw, SHA256: digest, Size: size, Source: "upstream", State: "active", Format: meta.Format, Identity: meta.Identity}
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		return err
	}
	if err := cache.VerifyFile(name, p); err != nil {
		return err
	}
	if replaceIndex >= 0 {
		old := m.Packages[replaceIndex]
		if !sameIdentity(old, p) || old.SHA256 == p.SHA256 {
			return errors.New("replacement needs different bytes with the same identity")
		}
		if old.State == "active" {
			m.Packages[replaceIndex].State = "retained"
			m.Packages[replaceIndex].RetainedAt = now.UTC().Format(time.RFC3339)
		}
		m.Packages = append(m.Packages, p)
		if err := m.Validate(r); err != nil {
			return err
		}
		return os.Rename(name, filepath.Join(dir, digest))
	}
	for _, old := range m.Packages {
		if old.State != "active" || !sameIdentity(old, p) {
			continue
		}
		if old.SHA256 != p.SHA256 || old.Size != p.Size {
			return errors.New("package identity already has different bytes")
		}
		return nil
	}
	for _, old := range m.Packages {
		if sameIdentity(old, p) {
			return errors.New("package identity is retained; use an explicit replacement operation")
		}
	}
	m.Packages = append(m.Packages, p)
	if err := m.Validate(r); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, digest))
}
func sameIdentity(a, b contract.Package) bool {
	return a.Format == b.Format && a.Identity == b.Identity
}
func (c Runner) sync(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("sync takes no arguments")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	_, _, root := c.paths()
	for _, p := range m.Packages {
		if _, err := cache.Ensure(ctx, root, r, p, c.Fetcher); err != nil {
			return err
		}
	}
	return nil
}
func (c Runner) sourceSet(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: ghrepo source set SELECTOR URL")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	i, err := selectOne(m, args[0])
	if err != nil {
		return err
	}
	if err := contract.ValidateSourceURL(args[1], r.Downloads.SourceHosts); err != nil {
		return err
	}
	if m.Packages[i].URL == args[1] {
		return nil
	}
	m.Packages[i].URL = args[1]
	_, _, root := c.paths()
	if _, err := cache.FetchVerified(ctx, root, r, m.Packages[i], c.Fetcher); err != nil {
		return err
	}
	return c.save(m, r)
}
func (c Runner) remove(args []string) error {
	if len(args) == 0 {
		return errors.New("remove requires selectors")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	for _, s := range args {
		i, err := selectOne(m, s)
		if err != nil {
			return err
		}
		if seen[i] {
			return errors.New("duplicate selector")
		}
		seen[i] = true
	}
	for i := range seen {
		if m.Packages[i].State == "active" {
			m.Packages[i].State = "retained"
			m.Packages[i].RetainedAt = c.now().Format(time.RFC3339)
		}
	}
	return c.save(m, r)
}
func selectOne(m contract.Manifest, s string) (int, error) {
	index := -1
	for i, p := range m.Packages {
		v := p.Format + "/" + p.Identity.Name + "/" + p.Identity.Architecture + "/"
		if p.Format == "rpm" {
			v += p.Identity.Epoch + "/"
		}
		v += p.Identity.Version
		if p.Format == "rpm" {
			v += "/" + p.Identity.Release
		}
		if s == p.Path || s == v {
			if index >= 0 {
				return -1, errors.New("ambiguous selector")
			}
			index = i
		}
	}
	if index < 0 {
		return -1, fmt.Errorf("package selector not found: %s", s)
	}
	return index, nil
}

// This revision contains the manifest rules used by generated data-only sites.
const siteBuilderRevision = "d515279a6ac6145aea1e299f51a781c1a89c1272"

func workflowFor(r contract.Repository) string {
	return "name: Pages\n" +
		"on:\n  push:\n    branches: [\"" + r.Repository.Branch + "\"]\n  workflow_dispatch:\n" +
		"concurrency:\n  group: pages-" + r.Repository.Branch + "\n  cancel-in-progress: false\n" +
		"permissions:\n  contents: read\n" +
		"jobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - uses: actions/checkout@v4\n" +
		"      - uses: actions/setup-go@v5\n        with:\n          go-version: '1.25.5'\n" +
		"      - name: Build site from this commit or pinned builder\n" +
		"        run: |\n" +
		"          if [ -f go.mod ] && grep -qx 'module github.com/asciimoth/ghrepo' go.mod; then\n" +
		"            go run . site-build\n" +
		"          else\n" +
		"            go run github.com/asciimoth/ghrepo@" + siteBuilderRevision + " site-build\n" +
		"          fi\n" +
		"      - uses: actions/upload-pages-artifact@v3\n        with:\n          path: build/site\n" +
		"  deploy:\n    needs: build\n    runs-on: ubuntu-latest\n" +
		"    environment:\n      name: github-pages\n      url: ${{ steps.deployment.outputs.page_url }}\n" +
		"    permissions:\n      pages: write\n      id-token: write\n" +
		"    steps:\n      - id: deployment\n        uses: actions/deploy-pages@v4\n"
}
