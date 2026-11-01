package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asciimoth/ghrepo/apt"
	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/rpm"
	"github.com/asciimoth/ghrepo/site"
)

func (c Runner) output() io.Writer {
	if c.Out != nil {
		return c.Out
	}
	return os.Stdout
}
func (c Runner) transaction() string { return filepath.Join(c.root(), ".ghrepo-transaction") }

type transactionRecord struct {
	HadDeb bool `json:"had_deb"`
	HadRPM bool `json:"had_rpm"`
}

// recoverTransaction restores the previous complete pair if a swap was interrupted.
func (c Runner) recoverTransaction() error {
	dir := c.transaction()
	data, err := os.ReadFile(filepath.Join(dir, "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(dir)
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "complete")); err == nil {
		return os.RemoveAll(dir)
	}
	var rec transactionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		had  bool
	}{{"deb", rec.HadDeb}, {"rpm", rec.HadRPM}} {
		target := filepath.Join(c.root(), item.name)
		backup := filepath.Join(dir, "old-"+item.name)
		if _, err := os.Stat(backup); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := os.Rename(backup, target); err != nil {
				return err
			}
		} else if !item.had {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(dir)
}

func (c Runner) local() (contract.Local, error) {
	return contract.LoadEffectiveLocal(filepath.Join(c.root(), ".ghrepo.local.yaml"), processEnv())
}

func (c Runner) rebuild(ctx context.Context) error {
	if err := c.recoverTransaction(); err != nil {
		return err
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	local, err := c.local()
	if err != nil {
		return err
	}
	if local.GPG.KeyID == "" {
		return errors.New("signing key is not configured")
	}
	_, _, cacheRoot := c.paths()
	for _, p := range m.Packages {
		if _, err := cache.Ensure(ctx, cacheRoot, r, p, c.Fetcher); err != nil {
			return err
		}
	}
	dir := c.transaction()
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	recorded := false
	defer func() {
		if !recorded {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := apt.Build(filepath.Join(dir, "new-deb"), cacheRoot, r, m, local); err != nil {
		return err
	}
	if err := rpm.BuildWithKeyRoot(filepath.Join(dir, "new-rpm"), cacheRoot, c.root(), r, m, local); err != nil {
		return err
	}
	if err := c.retainRPM(filepath.Join(c.root(), "rpm", "repodata"), filepath.Join(dir, "new-rpm", "repodata"), r.Site.RetentionDays); err != nil {
		return err
	}
	key := filepath.Join(c.root(), "KEY.gpg")
	if err := apt.Verify(filepath.Join(dir, "new-deb"), key, r, m); err != nil {
		return err
	}
	// RPM verification reads package files from the site root. Build a private
	// regular-file tree from the verified cache for this check.
	packages := filepath.Join(dir, "packages")
	for _, p := range m.Packages {
		if p.Format == "rpm" && p.State == "active" {
			dst := filepath.Join(packages, filepath.FromSlash(p.Path))
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			if err := copyPackage(filepath.Join(cacheRoot, "sha256", p.SHA256), dst); err != nil {
				return err
			}
		}
	}
	if err := c.stageTrustedKeys(r, packages); err != nil {
		return err
	}
	if err := rpm.Verify(filepath.Join(dir, "new-rpm"), key, packages, r, m); err != nil {
		return err
	}
	rec := transactionRecord{}
	for _, item := range []struct {
		name string
		had  *bool
	}{{"deb", &rec.HadDeb}, {"rpm", &rec.HadRPM}} {
		_, e := os.Lstat(filepath.Join(c.root(), item.name))
		if e == nil {
			*item.had = true
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := contract.WriteAtomic(filepath.Join(dir, "pending.json"), data, 0600); err != nil {
		return err
	}
	recorded = true
	for _, name := range []string{"deb", "rpm"} {
		target := filepath.Join(c.root(), name)
		if _, err := os.Lstat(target); err == nil {
			if err := os.Rename(target, filepath.Join(dir, "old-"+name)); err != nil {
				_ = c.recoverTransaction()
				return err
			}
		}
		if err := os.Rename(filepath.Join(dir, "new-"+name), target); err != nil {
			_ = c.recoverTransaction()
			return err
		}
	}
	if err := contract.WriteAtomic(filepath.Join(dir, "complete"), []byte("ok\n"), 0600); err != nil {
		_ = c.recoverTransaction()
		return err
	}
	return os.RemoveAll(dir)
}

var oldRPMName = regexp.MustCompile(`^[a-f0-9]{64}-(primary|filelists|other)\.xml\.gz$`)

func (c Runner) retainRPM(old, next string, days int) error {
	var record map[string]string
	data, err := os.ReadFile(filepath.Join(old, "retention.json"))
	if err == nil {
		if err := json.Unmarshal(data, &record); err != nil {
			return errors.New("invalid RPM retention record")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if record == nil {
		record = map[string]string{}
	}
	protected, err := c.protectedRPMMetadata()
	if err != nil {
		return err
	}
	nextEntries, err := os.ReadDir(next)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	seen := map[string]bool{}
	for _, e := range nextEntries {
		if oldRPMName.MatchString(e.Name()) {
			current[e.Name()] = true
			seen[e.Name()] = true
			if record[e.Name()] == "" {
				record[e.Name()] = c.now().Format(time.RFC3339)
			}
		}
	}
	entries, err := os.ReadDir(old)
	if errors.Is(err, os.ErrNotExist) {
		return writeRPMRetention(next, record)
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !oldRPMName.MatchString(e.Name()) {
			continue
		}
		seen[e.Name()] = true
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 128<<20 {
			return errors.New("invalid retained RPM metadata file")
		}
		if record[e.Name()] == "" {
			record[e.Name()] = c.now().Format(time.RFC3339)
		}
		first, parseErr := time.Parse(time.RFC3339, record[e.Name()])
		if parseErr != nil || first.Location() != time.UTC {
			return errors.New("invalid RPM retention timestamp")
		}
		if !current[e.Name()] && !protected[e.Name()] && !c.now().Before(first.AddDate(0, 0, days)) {
			delete(record, e.Name())
			continue
		}
		src, dst := filepath.Join(old, e.Name()), filepath.Join(next, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := copyPackage(src, dst); err != nil {
			return err
		}
		f, err := os.Open(dst)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(f, 128<<20+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > 128<<20 || fmt.Sprintf("%x", hash.Sum(nil)) != e.Name()[:64] {
			return errors.New("retained RPM metadata checksum mismatch")
		}
	}
	for name := range record {
		if !seen[name] {
			delete(record, name)
		}
	}
	return writeRPMRetention(next, record)
}
func copyPackage(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("package input is not a regular file")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	return err
}

func (c Runner) stageTrustedKeys(r contract.Repository, root string) error {
	for _, key := range r.RPM.TrustedKeyPaths {
		dst := filepath.Join(root, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := copyPackage(filepath.Join(c.root(), filepath.FromSlash(key)), dst); err != nil {
			return err
		}
	}
	return nil
}

func (c Runner) check(ctx context.Context) error {
	if err := c.recoverTransaction(); err != nil {
		return err
	}
	r, m, err := c.load()
	if err != nil {
		return err
	}
	if _, err := c.local(); err != nil {
		return err
	}
	_, _, cacheRoot := c.paths()
	for _, p := range m.Packages {
		if _, err := cache.FetchVerified(ctx, cacheRoot, r, p, c.Fetcher); err != nil {
			return err
		}
	}
	key := filepath.Join(c.root(), "KEY.gpg")
	if err := apt.Verify(filepath.Join(c.root(), "deb"), key, r, m); err != nil {
		return err
	}
	// Verify RPM indexed bytes against the cache in a temporary package tree.
	temp, err := os.MkdirTemp("", "ghrepo-check-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	for _, p := range m.Packages {
		if p.Format == "rpm" && p.State == "active" {
			dst := filepath.Join(temp, filepath.FromSlash(p.Path))
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			if err := copyPackage(filepath.Join(cacheRoot, "sha256", p.SHA256), dst); err != nil {
				return err
			}
		}
	}
	if err := c.stageTrustedKeys(r, temp); err != nil {
		return err
	}
	if err := rpm.Verify(filepath.Join(c.root(), "rpm"), key, temp, r, m); err != nil {
		return err
	}
	// The site builder checks all links and the exact rendered size.
	return site.Build(ctx, c.root(), filepath.Join(temp, "site"), c.Fetcher)
}

func (c Runner) publish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("dry-run", false, "show intended Git changes")
	message := fs.String("message", "chore(repo): publish signed metadata", "commit message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: ghrepo publish [--dry-run] [--message TEXT]")
	}
	if *dry {
		previewDir, err := copyPreviewRepository(c.root())
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(previewDir) }()
		c.Dir = previewDir
	}
	if err := c.verifyIndexAllowlist(); err != nil {
		return err
	}
	if err := c.sync(ctx, nil); err != nil {
		return err
	}
	if err := c.rebuild(ctx); err != nil {
		return err
	}
	if err := c.check(ctx); err != nil {
		return err
	}
	if err := site.RenderReadme(c.root()); err != nil {
		return err
	}
	paths, err := c.allowedGitPaths()
	if err != nil {
		return err
	}
	if err := c.verifyIndexAllowlist(); err != nil {
		return err
	}
	changes, tree, err := c.intendedChanges(paths)
	if err != nil {
		return err
	}
	if *dry {
		for _, item := range changes {
			if _, err := fmt.Fprintln(c.output(), item); err != nil {
				return err
			}
		}
		return nil
	}
	if len(changes) == 0 {
		return c.git("push", "origin", "HEAD")
	}
	if err := c.git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	if err := c.verifyIndexAllowlist(); err != nil {
		return err
	}
	stagedTree, err := c.gitOutput("write-tree")
	if err != nil {
		return err
	}
	if string(stagedTree) != tree {
		return errors.New("git index changed after dry-run calculation")
	}
	if err := c.git("commit", "-m", *message); err != nil {
		return err
	}
	if err := c.git("push", "origin", "HEAD"); err != nil {
		return err
	}
	return nil
}

// copyPreviewRepository gives publish its own worktree and Git database.
func copyPreviewRepository(root string) (string, error) {
	previewDir, err := os.MkdirTemp("", "ghrepo-publish-*")
	if err != nil {
		return "", err
	}
	cleanup := func(err error) (string, error) {
		_ = os.RemoveAll(previewDir)
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return cleanup(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return cleanup(err)
	}
	if rel, err := filepath.Rel(root, previewDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return cleanup(errors.New("preview directory must be outside the repository"))
	}
	git, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil {
		return cleanup(err)
	}
	if !git.IsDir() {
		return cleanup(errors.New("publish dry run requires a local .git directory"))
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		destination := filepath.Join(previewDir, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Mkdir(destination, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			target := link
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			target, err = filepath.EvalSymlinks(target)
			if err != nil {
				return err
			}
			relTarget, err := filepath.Rel(root, target)
			if err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
				return fmt.Errorf("preview symlink points outside repository: %s", rel)
			}
			if filepath.IsAbs(link) {
				link = filepath.Join(previewDir, relTarget)
			}
			return os.Symlink(link, destination)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("publish dry run cannot copy non-regular file: %s", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		return err
	})
	if err != nil {
		return cleanup(err)
	}
	return previewDir, nil
}

func (c Runner) git(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = c.root()
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return nil
}
func (c Runner) gitOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = c.root()
	return cmd.Output()
}

func (c Runner) allowedGitPaths() ([]string, error) {
	r, _, err := c.load()
	if err != nil {
		return nil, err
	}
	tracked, err := c.gitOutput("ls-files", "-z")
	if err != nil {
		return nil, err
	}
	trackedSet := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSuffix(string(tracked), "\x00"), "\x00") {
		if p != "" {
			trackedSet[p] = true
		}
	}
	paths := []string{"ghrepo.yaml", "packages.yaml", "README.md", "KEY.gpg", ".gitignore", ".github/workflows/pages.yml", "deb/Packages", "deb/Packages.gz", "deb/Release", "deb/InRelease", "rpm/repodata/repomd.xml", "rpm/repodata/repomd.xml.asc", "rpm/repodata/retention.json"}
	for _, name := range []string{"index.html", "README.md", "setup.sh", "deb.list", "rpm.repo"} {
		path := "templates/" + name
		if _, err := os.Lstat(filepath.Join(c.root(), path)); err == nil || (errors.Is(err, os.ErrNotExist) && trackedSet[path]) {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	paths = append(paths, r.RPM.TrustedKeyPaths...)
	entries, err := os.ReadDir(filepath.Join(c.root(), "rpm", "repodata"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if oldRPMName.MatchString(e.Name()) {
			paths = append(paths, "rpm/repodata/"+e.Name())
		}
	}
	for p := range trackedSet {
		if strings.HasPrefix(p, "rpm/repodata/") && oldRPMName.MatchString(strings.TrimPrefix(p, "rpm/repodata/")) {
			paths = append(paths, p)
		}
	}
	seen := map[string]bool{}
	unique := paths[:0]
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if !c.permittedGitPath(p) {
			return nil, fmt.Errorf("git path is not allowed: %s", p)
		}
		unique = append(unique, p)
	}
	return unique, nil
}
func safeGitPath(p string) bool {
	if p == "ghrepo.yaml" || p == "packages.yaml" || p == "README.md" || p == "KEY.gpg" || p == ".gitignore" || p == ".github/workflows/pages.yml" {
		return true
	}
	if strings.HasPrefix(p, "templates/") {
		return p == "templates/index.html" || p == "templates/README.md" || p == "templates/setup.sh" || p == "templates/deb.list" || p == "templates/rpm.repo"
	}
	if p == "deb/Packages" || p == "deb/Packages.gz" || p == "deb/Release" || p == "deb/InRelease" || p == "rpm/repodata/repomd.xml" || p == "rpm/repodata/repomd.xml.asc" || p == "rpm/repodata/retention.json" {
		return true
	}
	return strings.HasPrefix(p, "rpm/repodata/") && oldRPMName.MatchString(strings.TrimPrefix(p, "rpm/repodata/"))
}
func (c Runner) permittedGitPath(p string) bool {
	if safeGitPath(p) {
		return true
	}
	r, err := contract.ReadRepository(filepath.Join(c.root(), "ghrepo.yaml"))
	if err != nil {
		return false
	}
	for _, key := range r.RPM.TrustedKeyPaths {
		if p == key && p != "local-cache" && p != "build" && p != ".ghrepo-transaction" && !strings.HasPrefix(p, "local-cache/") && !strings.HasPrefix(p, "build/") && !strings.HasPrefix(p, ".ghrepo-transaction/") {
			return true
		}
	}
	return false
}
func (c Runner) verifyIndexAllowlist() error {
	data, err := c.gitOutput("diff", "--cached", "--no-renames", "--name-only", "-z")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00") {
		if p != "" && !c.permittedGitPath(p) {
			return fmt.Errorf("staged path is not allowed: %s", p)
		}
	}
	return nil
}
func (c Runner) gitWithIndex(index string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = c.root()
	command.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	return command.Output()
}

// intendedChanges stages into a private copy of the index. The returned tree
// identifies the exact commit contents that a real publish must reproduce.
func (c Runner) intendedChanges(paths []string) ([]string, string, error) {
	indexPath, err := c.gitOutput("rev-parse", "--git-path", "index")
	if err != nil {
		return nil, "", err
	}
	index := strings.TrimSpace(string(indexPath))
	if !filepath.IsAbs(index) {
		index = filepath.Join(c.root(), index)
	}
	temp, err := os.CreateTemp("", "ghrepo-index-*")
	if err != nil {
		return nil, "", err
	}
	name := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return nil, "", err
	}
	defer func() { _ = os.Remove(name) }()
	data, err := os.ReadFile(index)
	if err == nil {
		if err := os.WriteFile(name, data, 0600); err != nil {
			return nil, "", err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Remove(name); err != nil {
			return nil, "", err
		}
	} else {
		return nil, "", err
	}
	if _, err := c.gitWithIndex(name, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return nil, "", fmt.Errorf("git add preview failed: %w", err)
	}
	changed, err := c.gitWithIndex(name, "diff", "--cached", "--no-renames", "--name-status", "-z")
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00")
	var result []string
	for i := 0; i+1 < len(parts); i += 2 {
		status, path := parts[i], parts[i+1]
		if !c.permittedGitPath(path) {
			return nil, "", fmt.Errorf("git path is not allowed: %s", path)
		}
		result = append(result, status+" "+path)
	}
	tree, err := c.gitWithIndex(name, "write-tree")
	if err != nil {
		return nil, "", err
	}
	return result, string(tree), nil
}
