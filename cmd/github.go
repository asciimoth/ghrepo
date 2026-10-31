package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	"github.com/asciimoth/ghrepo/rpm"
	"github.com/asciimoth/ghrepo/storage"
)

func processEnv() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func (c Runner) githubFor(r contract.Repository, local contract.Local) (*storage.GitHub, error) {
	u, err := url.Parse(r.Repository.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return nil, errors.New("GitHub repository URL is required")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, errors.New("invalid GitHub repository URL")
	}
	account, repository := parts[0], parts[1]
	if local.GitHub.Account != "" && local.GitHub.Account != account {
		return nil, errors.New("GitHub account does not match repository URL")
	}
	return &storage.GitHub{Account: account, Repository: repository, Prefix: r.Storage.ReleasePrefix, BucketSize: r.Storage.BucketSize, Token: local.GitHub.Token, APIURL: c.GitHubAPIURL, Client: c.GitHubClient, Fetcher: c.Fetcher, RedirectHosts: r.Downloads.RedirectHosts}, nil
}

func (c Runner) storageGitHubFor(r contract.Repository, local contract.Local) (*storage.GitHub, error) {
	g, err := c.githubFor(r, local)
	if err != nil {
		return nil, err
	}
	if local.Storage.ReleaseRepository != "" {
		p := strings.Split(local.Storage.ReleaseRepository, "/")
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			return nil, errors.New("invalid release storage repository")
		}
		g.Account, g.Repository = p[0], p[1]
	}
	return g, nil
}

type uploadProgress struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

func (c Runner) addUpload(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	upload := fs.Bool("upload", false, "upload local package files")
	sign := fs.Bool("sign-rpm", false, "sign an RPM copy before upload")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*upload || fs.NArg() == 0 {
		return errors.New("usage: ghrepo add --upload [--sign-rpm] FILE")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	if r.Storage.Backend != "github-releases" {
		return errors.New("GitHub Releases storage is not enabled")
	}
	if err := contract.ValidateSourceURL("https://github.com/example/repo/releases/download/tag/asset", r.Downloads.SourceHosts); err != nil {
		return errors.New("github.com must be an allowed source host for uploads")
	}
	local, err := c.local()
	if err != nil {
		return err
	}
	g, err := c.storageGitHubFor(r, local)
	if err != nil {
		return err
	}
	if c.dryRun {
		for _, file := range fs.Args() {
			info, err := os.Lstat(file)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > r.Downloads.MaxBytes {
				return errors.New("invalid upload file")
			}
			meta, _, _, err := inspect.File(file, r.Downloads.MaxBytes)
			if err != nil {
				return err
			}
			if *sign && meta.Format != "rpm" {
				return errors.New("--sign-rpm requires RPM files")
			}
			if err := c.preview("would upload %s", file); err != nil {
				return err
			}
		}
		return nil
	}
	for _, file := range fs.Args() {
		if err := c.uploadOne(ctx, file, *sign, g, r, local, &m); err != nil {
			return err
		}
	}
	if err := c.save(m, r); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(c.root(), ".ghrepo-upload"))
}

func (c Runner) uploadOne(ctx context.Context, file string, sign bool, g *storage.GitHub, r contract.Repository, local contract.Local, m *contract.Manifest) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > r.Downloads.MaxBytes {
		return errors.New("invalid upload file")
	}
	inputMeta, _, _, err := inspect.File(file, r.Downloads.MaxBytes)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(r.RPM.TrustedKeyPaths))
	for _, key := range r.RPM.TrustedKeyPaths {
		keys = append(keys, filepath.Join(c.root(), filepath.FromSlash(key)))
	}
	if inputMeta.Format == "rpm" && r.RPM.PackageSignature == "trusted-packages" && !sign {
		if err := rpm.VerifyPackageSignature(file, keys, "trusted-packages"); err != nil {
			if local.GPG.KeyID == "" {
				return err
			}
			sign = true
		}
	}
	source := file
	if sign {
		if inputMeta.Format != "rpm" {
			return errors.New("--sign-rpm requires RPM files")
		}
		dir, err := os.MkdirTemp("", "ghrepo-sign-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		source = filepath.Join(dir, "signed.rpm")
		if err := rpm.SignCopy(file, source, local, keys); err != nil {
			return err
		}
	}
	meta, size, digest, err := inspect.File(source, r.Downloads.MaxBytes)
	if err != nil {
		return err
	}
	if meta.Format == "rpm" && r.RPM.PackageSignature == "trusted-packages" {
		if err := rpm.VerifyPackageSignature(source, keys, "trusted-packages"); err != nil {
			return err
		}
	}
	ext := ".deb"
	if meta.Format == "rpm" {
		ext = ".rpm"
	}
	name := digest + ext
	p := contract.Package{SHA256: digest, Size: size, Source: "owned", State: "active", Format: meta.Format, Identity: meta.Identity}
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		return err
	}
	existing := -1
	for i, old := range m.Packages {
		if old.State != "active" || !sameIdentity(old, p) {
			continue
		}
		if old.SHA256 != p.SHA256 || old.Size != p.Size {
			return errors.New("package identity already has different bytes")
		}
		existing = i
		break
	}
	if existing < 0 {
		for i, old := range m.Packages {
			if !sameIdentity(old, p) {
				continue
			}
			if old.SHA256 != p.SHA256 || old.Size != p.Size {
				return errors.New("package identity already has different bytes")
			}
			existing = i
			break
		}
	}
	progressDir := filepath.Join(c.root(), ".ghrepo-upload")
	progressPath := filepath.Join(progressDir, digest+".json")
	publicURL := ""
	if saved, readErr := os.ReadFile(progressPath); readErr == nil {
		var record uploadProgress
		if json.Unmarshal(saved, &record) == nil && record.Name == name && record.SHA256 == digest && g.VerifyOwnedAsset(ctx, record.URL, name, size, digest) == nil {
			publicURL = record.URL
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if publicURL == "" {
		publicURL, err = g.PutFile(ctx, name, source, size, digest)
		if err != nil {
			return err
		}
	}
	p.URL = publicURL
	if err := contract.ValidateSourceURL(p.URL, r.Downloads.SourceHosts); err != nil {
		return errors.New("uploaded URL is not allowed by source hosts")
	}
	if err := os.MkdirAll(progressDir, 0700); err != nil {
		return err
	}
	progress, _ := json.Marshal(uploadProgress{Name: name, SHA256: digest, URL: publicURL})
	if err := contract.WriteAtomic(progressPath, progress, 0600); err != nil {
		return err
	}
	_, _, cacheRoot := c.paths()
	cachePath := filepath.Join(cacheRoot, "sha256", digest)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
		return err
	}
	if err := copyUploadedCache(source, cachePath, p); err != nil {
		return err
	}
	if existing >= 0 {
		m.Packages[existing].URL = p.URL
		m.Packages[existing].Source = "owned"
	} else {
		m.Packages = append(m.Packages, p)
	}
	return m.Validate(r)
}

func copyUploadedCache(source, destination string, p contract.Package) error {
	if cache.VerifyFile(destination, p) == nil {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != p.Size {
		return errors.New("upload file changed")
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	n, copyErr := io.Copy(temp, io.LimitReader(in, p.Size+1))
	if copyErr != nil || n != p.Size {
		_ = temp.Close()
		return errors.New("upload file changed")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := cache.VerifyFile(temp.Name(), p); err != nil {
		return err
	}
	return os.Rename(temp.Name(), destination)
}

func (c Runner) gc(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ghrepo gc")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	if r.Storage.Backend != "github-releases" {
		return errors.New("GitHub Releases storage is not enabled")
	}
	local, err := c.local()
	if err != nil {
		return err
	}
	g, err := c.storageGitHubFor(r, local)
	if err != nil {
		return err
	}
	referenced := map[string]bool{}
	assetKey := func(raw string) string {
		tag, name, ok := g.AssetURLParts(raw)
		if !ok {
			return ""
		}
		return tag + "/" + name
	}
	for _, p := range m.Packages {
		if p.Source == "owned" {
			if key := assetKey(p.URL); key != "" {
				referenced[key] = true
			}
		}
	}
	refs, err := c.gitOutput("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return err
	}
	for _, ref := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
		if ref == "" {
			continue
		}
		paths := exec.Command("git", "ls-tree", "--name-only", ref, "--", "packages.yaml")
		paths.Dir = c.root()
		listed, e := paths.Output()
		if e != nil {
			return errors.New("cannot inspect rollback reference")
		}
		if strings.TrimSpace(string(listed)) == "" {
			continue
		}
		command := exec.Command("git", "show", ref+":packages.yaml")
		command.Dir = c.root()
		data, e := command.Output()
		if e != nil {
			return errors.New("cannot read rollback manifest")
		}
		config := exec.Command("git", "show", ref+":ghrepo.yaml")
		config.Dir = c.root()
		configData, e := config.Output()
		if e != nil {
			return errors.New("cannot read rollback repository settings")
		}
		oldRepository, e := contract.ParseRepository(configData)
		if e != nil {
			return errors.New("cannot validate rollback repository settings")
		}
		old, e := contract.ParseManifest(data, oldRepository)
		if e != nil {
			return errors.New("cannot validate rollback manifest")
		}
		for _, p := range old.Packages {
			if p.Source == "owned" {
				if key := assetKey(p.URL); key != "" {
					referenced[key] = true
				}
			}
		}
	}
	assets, err := g.ListOwnedAssets(ctx)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if !referenced[assetKey(a.URL)] {
			if _, err := fmt.Fprintf(c.output(), "unreferenced %s %s\n", a.ReleaseTag, a.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
