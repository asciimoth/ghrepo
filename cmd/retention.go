package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/rpm"
)

func (c Runner) replacePackage(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: ghrepo replace SELECTOR URL")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	i, err := selectOne(m, args[0])
	if err != nil {
		return err
	}
	_, _, root := c.paths()
	dir := filepath.Join(root, "sha256")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	fetcher := c.Fetcher
	if fetcher == nil {
		fetcher = cache.HTTPFetcher{}
	}
	if err := addOne(ctx, args[1], dir, r, &m, fetcher, i, c.now()); err != nil {
		return err
	}
	newPackage := m.Packages[len(m.Packages)-1]
	if newPackage.Format == "rpm" && r.RPM.PackageSignature == "trusted-packages" {
		keys := make([]string, len(r.RPM.TrustedKeyPaths))
		for i, key := range r.RPM.TrustedKeyPaths {
			keys[i] = filepath.Join(c.root(), filepath.FromSlash(key))
		}
		if err := rpm.VerifyPackageSignature(filepath.Join(dir, newPackage.SHA256), keys, r.RPM.PackageSignature); err != nil {
			return err
		}
	}
	return c.save(m, r)
}

func (c Runner) prune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apply := fs.Bool("apply", false, "remove eligible entries")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: ghrepo prune [--apply]")
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	protected, err := c.protectedPackagePaths()
	if err != nil {
		return err
	}
	kept := make([]contract.Package, 0, len(m.Packages))
	changed := false
	for _, p := range m.Packages {
		eligible := false
		if p.State == "retained" {
			retained, _ := time.Parse(time.RFC3339, p.RetainedAt)
			eligible = !c.now().Before(retained.AddDate(0, 0, r.Site.RetentionDays)) && !protected[p.Path]
			if eligible {
				inIndex, e := c.indexReferences(p)
				if e != nil {
					return e
				}
				eligible = !inIndex
			}
		}
		if eligible {
			if _, err := fmt.Fprintf(c.output(), "eligible %s\n", p.Path); err != nil {
				return err
			}
			if *apply {
				changed = true
				continue
			}
		}
		kept = append(kept, p)
	}
	if changed {
		m.Packages = kept
		return c.save(m, r)
	}
	return nil
}

func (c Runner) protectedPackagePaths() (map[string]bool, error) {
	protected := map[string]bool{}
	if _, err := os.Stat(filepath.Join(c.root(), ".git")); errors.Is(err, os.ErrNotExist) {
		return protected, nil
	} else if err != nil {
		return nil, err
	}
	refs, err := c.gitOutput("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, errors.New("cannot inspect rollback references")
	}
	current, _ := c.gitOutput("symbolic-ref", "-q", "HEAD")
	for _, ref := range strings.Fields(string(refs)) {
		if ref == strings.TrimSpace(string(current)) {
			continue
		}
		cmd := exec.Command("git", "show", ref+":packages.yaml")
		cmd.Dir = c.root()
		data, err := cmd.Output()
		if err != nil {
			continue
		} // A reference can predate the manifest.
		cmd = exec.Command("git", "show", ref+":ghrepo.yaml")
		cmd.Dir = c.root()
		config, err := cmd.Output()
		if err != nil {
			return nil, errors.New("cannot read rollback repository settings")
		}
		r, err := contract.ParseRepository(config)
		if err != nil {
			return nil, errors.New("invalid rollback repository settings")
		}
		m, err := contract.ParseManifest(data, r)
		if err != nil {
			return nil, errors.New("invalid rollback manifest")
		}
		for _, p := range m.Packages {
			protected[p.Path] = true
		}
	}
	return protected, nil
}

func (c Runner) indexReferences(p contract.Package) (bool, error) {
	if p.Format == "deb" {
		data, err := os.ReadFile(filepath.Join(c.root(), "deb", "Packages"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return bytes.Contains(data, []byte("Filename: "+strings.TrimPrefix(p.Path, "deb/")+"\n")), nil
	}
	dir := filepath.Join(c.root(), "rpm", "repodata")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "-primary.xml.gz") || !oldRPMName.MatchString(e.Name()) {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		z, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return false, err
		}
		data, err := io.ReadAll(io.LimitReader(z, 128<<20+1))
		_ = z.Close()
		_ = f.Close()
		if err != nil {
			return false, err
		}
		if len(data) > 128<<20 {
			return false, errors.New("RPM metadata exceeds size limit")
		}
		if bytes.Contains(data, []byte(strings.TrimPrefix(p.Path, "rpm/"))) {
			return true, nil
		}
	}
	return false, nil
}

func writeRPMRetention(dir string, record map[string]string) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return contract.WriteAtomic(filepath.Join(dir, "retention.json"), append(data, '\n'), 0644)
}

func (c Runner) protectedRPMMetadata() (map[string]bool, error) {
	protected := map[string]bool{}
	if _, err := os.Stat(filepath.Join(c.root(), ".git")); errors.Is(err, os.ErrNotExist) {
		return protected, nil
	} else if err != nil {
		return nil, err
	}
	refs, err := c.gitOutput("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, errors.New("cannot inspect RPM rollback references")
	}
	current, _ := c.gitOutput("symbolic-ref", "-q", "HEAD")
	for _, ref := range strings.Fields(string(refs)) {
		if ref == strings.TrimSpace(string(current)) {
			continue
		}
		cmd := exec.Command("git", "show", ref+":rpm/repodata/repomd.xml")
		cmd.Dir = c.root()
		data, err := cmd.Output()
		if err != nil {
			continue // A reference can predate RPM metadata.
		}
		var index struct {
			XMLName xml.Name `xml:"repomd"`
			Entries []struct {
				Location struct {
					Href string `xml:"href,attr"`
				} `xml:"location"`
			} `xml:"data"`
		}
		if err := xml.Unmarshal(data, &index); err != nil || len(index.Entries) == 0 {
			return nil, errors.New("invalid RPM rollback index")
		}
		for _, entry := range index.Entries {
			path := entry.Location.Href
			if !strings.HasPrefix(path, "repodata/") {
				return nil, errors.New("invalid RPM rollback metadata path")
			}
			name := strings.TrimPrefix(path, "repodata/")
			if !oldRPMName.MatchString(name) {
				return nil, errors.New("invalid RPM rollback metadata path")
			}
			protected[name] = true
		}
	}
	return protected, nil
}
