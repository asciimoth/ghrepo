// Package site builds a Pages artifact from committed files and public sources.
package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	texttemplate "text/template"

	"github.com/asciimoth/ghrepo/apt"
	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/rpm"
)

var rpmIndexName = regexp.MustCompile(`^[a-f0-9]{64}-(primary|filelists|other)\.xml\.gz$`)

// Build creates and verifies a site before it replaces output. checkout needs
// only committed inputs; fetcher is nil for normal HTTPS downloads.
func Build(ctx context.Context, checkout, output string, fetcher cache.Fetcher) error {
	return build(ctx, checkout, output, fetcher, "")
}

// BuildFromVerified uses packages checked against public sources earlier in the
// same operation. It verifies cached bytes again before staging them.
func BuildFromVerified(ctx context.Context, checkout, output, verifiedRoot string, fetcher cache.Fetcher) error {
	return build(ctx, checkout, output, fetcher, verifiedRoot)
}

func build(ctx context.Context, checkout, output string, fetcher cache.Fetcher, verifiedRoot string) error {
	r, err := contract.ReadRepository(filepath.Join(checkout, "ghrepo.yaml"))
	if err != nil {
		return err
	}
	m, err := contract.ReadManifest(filepath.Join(checkout, "packages.yaml"), r)
	if err != nil {
		return err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".ghrepo-site-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	downloads := verifiedRoot
	if downloads == "" {
		downloads, err = os.MkdirTemp(parent, ".ghrepo-downloads-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(downloads) }()
	}
	if err := copyInputs(checkout, stage, r); err != nil {
		return err
	}
	for _, p := range m.Packages {
		var name string
		if verifiedRoot != "" {
			name = filepath.Join(downloads, "sha256", p.SHA256)
			err = cache.VerifyFile(name, p)
		} else {
			name, err = cache.FetchVerified(ctx, downloads, r, p, fetcher)
		}
		if err != nil {
			return fmt.Errorf("package %s: %w", p.Path, err)
		}
		if err := copyRegular(name, filepath.Join(stage, filepath.FromSlash(p.Path))); err != nil {
			return err
		}
		if err := cache.VerifyFile(filepath.Join(stage, filepath.FromSlash(p.Path)), p); err != nil {
			return fmt.Errorf("staged package %s: %w", p.Path, err)
		}
	}
	key := filepath.Join(stage, "KEY.gpg")
	if err := apt.Verify(filepath.Join(stage, "deb"), key, r, m); err != nil {
		return fmt.Errorf("APT metadata: %w", err)
	}
	if err := rpm.Verify(filepath.Join(stage, "rpm"), key, stage, r, m); err != nil {
		return fmt.Errorf("RPM metadata: %w", err)
	}
	if err := render(checkout, stage, r, m); err != nil {
		return err
	}
	if err := checkArtifact(stage, r.Site.MaxBytes); err != nil {
		return err
	}
	return replace(stage, output)
}

func copyInputs(checkout, stage string, r contract.Repository) error {
	for _, name := range []string{"KEY.gpg", "deb/Packages", "deb/Packages.gz", "deb/Release", "deb/InRelease", "rpm/repodata/repomd.xml", "rpm/repodata/repomd.xml.asc"} {
		if err := copyRegular(filepath.Join(checkout, filepath.FromSlash(name)), filepath.Join(stage, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(checkout, "rpm", "repodata"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !rpmIndexName.MatchString(entry.Name()) {
			continue
		}
		name := filepath.Join("rpm", "repodata", entry.Name())
		if err := copyRegular(filepath.Join(checkout, name), filepath.Join(stage, name)); err != nil {
			return err
		}
		file, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.Name()[:64] {
			return errors.New("RPM metadata filename checksum mismatch")
		}
	}
	for _, name := range r.RPM.TrustedKeyPaths {
		if name == "KEY.gpg" {
			continue
		}
		if err := copyRegular(filepath.Join(checkout, filepath.FromSlash(name)), filepath.Join(stage, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("trusted key %s: %w", name, err)
		}
	}
	return nil
}

func copyRegular(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || linked(info) {
		return errors.New("input is not a single-link regular file")
	}
	if err := checkParents(src); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	opened, err := in.Stat()
	if err != nil || !os.SameFile(info, opened) || linked(opened) {
		return errors.New("input changed while opening")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func checkParents(name string) error {
	// Reject symlinks in the path, including a symlinked checkout directory.
	for dir := filepath.Dir(name); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		parent, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
			return errors.New("input parent is not a directory")
		}
		if next := filepath.Dir(dir); next == dir {
			break
		}
	}
	return nil
}

func linked(info os.FileInfo) bool {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Nlink != 1
	}
	return false
}

func render(checkout, stage string, r contract.Repository, m contract.Manifest) error {
	parsed, err := url.Parse(r.Site.BaseURL)
	if err != nil {
		return err
	}
	baseURL := parsed.String()
	keys := []string{baseURL + "/KEY.gpg"}
	for _, key := range r.RPM.TrustedKeyPaths {
		if key != "KEY.gpg" {
			keys = append(keys, baseURL+"/"+key)
		}
	}
	gpgCheck := 0
	if r.RPM.PackageSignature == "trusted-packages" {
		gpgCheck = 1
	}
	values := struct {
		Repository contract.Repository
		Packages   []contract.Package
		BaseURL    string
		GPGCheck   int
		RPMKeyURLs string
	}{r, m.Packages, baseURL, gpgCheck, strings.Join(keys, " ")}
	for _, item := range DefaultTemplates {
		name := filepath.Join(checkout, "templates", item.Name)
		data, err := readOptionalTemplate(name)
		if errors.Is(err, os.ErrNotExist) && item.Name != "index.html" {
			continue
		}
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(stage, item.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		if item.Name == "index.html" {
			var t *htmltemplate.Template
			t, err = htmltemplate.New(item.Name).Option("missingkey=error").Parse(string(data))
			if err == nil {
				err = t.Execute(out, values)
			}
		} else {
			var t *texttemplate.Template
			t, err = texttemplate.New(item.Name).Funcs(texttemplate.FuncMap{"shellQuote": shellQuote}).Option("missingkey=error").Parse(string(data))
			if err == nil {
				err = t.Execute(out, values)
			}
		}
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("render %s: %w", item.Name, err)
		}
	}
	if r.Site.CNAME != "" {
		return os.WriteFile(filepath.Join(stage, "CNAME"), []byte(r.Site.CNAME+"\n"), 0644)
	}
	return nil
}

func readOptionalTemplate(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || linked(info) {
		return nil, errors.New("site template is not a single-link regular file")
	}
	if err := checkParents(name); err != nil {
		return nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || linked(opened) {
		return nil, errors.New("site template changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("site template exceeds size limit")
	}
	return data, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func checkArtifact(root string, limit int64) error {
	var size int64
	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || linked(info) {
			return fmt.Errorf("site contains a link or special file: %s", strings.TrimPrefix(name, root+string(filepath.Separator)))
		}
		if info.Size() > limit-size {
			return errors.New("site exceeds configured size limit")
		}
		size += info.Size()
		return nil
	})
}

func replace(stage, output string) error {
	old := output + ".previous"
	if _, err := os.Lstat(old); err == nil {
		return errors.New("previous site backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hadOld := false
	if _, err := os.Lstat(output); err == nil {
		if err := os.Rename(output, old); err != nil {
			return err
		}
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, output); err != nil {
		if hadOld {
			_ = os.Rename(old, output)
		}
		return err
	}
	if hadOld {
		return os.RemoveAll(old)
	}
	return nil
}
