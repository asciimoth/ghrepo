package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheCleanForcesRebuildDownloads(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	_, _, cacheRoot := runner.paths()
	metadata := filepath.Join(dir, "deb", "InRelease")
	before := mustRead(t, metadata)
	var out bytes.Buffer
	runner.Out = &out
	if err := runner.Run(ctx, []string{"cache", "clean", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove local cache") {
		t.Fatalf("missing dry-run report: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "sha256", manifest.Packages[0].SHA256)); err != nil {
		t.Fatalf("dry run removed cached package: %v", err)
	}
	if err := runner.Run(ctx, []string{"cache", "clean"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cacheRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache remains after clean: %v", err)
	}
	if !bytes.Equal(before, mustRead(t, metadata)) {
		t.Fatal("cache clean changed signed metadata")
	}
	if err := runner.Run(ctx, []string{"cache", "clean"}); err != nil {
		t.Fatalf("clean on absent cache failed: %v", err)
	}
	fetcher := &countingFetcher{base: runner.Fetcher}
	runner.Fetcher = fetcher
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if got := fetcher.calls.Load(); got != int32(len(manifest.Packages)) {
		t.Fatalf("rebuild fetched %d packages, want %d", got, len(manifest.Packages))
	}
}

func TestCacheCleanUsesSelectedRepository(t *testing.T) {
	selected := t.TempDir()
	other := t.TempDir()
	for _, dir := range []string{selected, other} {
		if err := os.Mkdir(filepath.Join(dir, "local-cache"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "local-cache", "entry"), []byte("cached"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := Runner{Dir: other, Out: &bytes.Buffer{}}
	if err := runner.Run(context.Background(), []string{"--repo-dir", selected, "cache", "clean"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selected, "local-cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected cache remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "local-cache", "entry")); err != nil {
		t.Fatalf("other repository cache changed: %v", err)
	}
	if err := runner.Run(context.Background(), []string{"cache", "clean", "extra"}); err == nil {
		t.Fatal("cache clean accepted an extra argument")
	}
}
