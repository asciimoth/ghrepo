package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/internal/progresstest"
)

type progressFetcher struct {
	t         *testing.T
	recorder  *progresstest.Recorder
	mu        sync.Mutex
	snapshots [][]progresstest.Node
	bad       bool
}

func (f *progressFetcher) record() {
	nodes := f.recorder.Snapshot(f.t)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, nodes)
}
func (f *progressFetcher) Fetch(context.Context, string, []string, []string) (io.ReadCloser, error) {
	f.record()
	data := "package-data"
	if f.bad {
		data = "invalid-data"
	}
	return &progressBody{reader: strings.NewReader(data), record: f.record}, nil
}

type progressBody struct {
	reader *strings.Reader
	record func()
}

func (r *progressBody) Read(p []byte) (int, error) {
	r.record()
	return r.reader.Read(p[:min(len(p), 3)])
}
func (*progressBody) Close() error { return nil }

func TestFetchProgressCountsAndCacheReuse(t *testing.T) {
	r := progresstest.New(t)
	packages := make([]contract.Package, 7)
	for i := range packages {
		p := testEntry(fmt.Sprintf("https://source.test/package-%d", i))
		p.Identity.Name = fmt.Sprintf("package-%d", i)
		p.Path, _ = contract.ServedPath(p)
		packages[i] = p
	}
	fetcher := &progressFetcher{t: t, recorder: r}
	root := t.TempDir()
	names, err := FetchAll(r.Context, root, testConfig(), packages, fetcher, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(packages) {
		t.Fatal(names)
	}
	foundPartial, foundComplete := false, false
	for _, nodes := range fetcher.snapshots {
		batch := progresstest.Find(t, nodes, "Fetch sources")
		if batch.Total != 7 || batch.Completed > 7 {
			t.Fatal("incorrect package count", batch)
		}
		for _, node := range nodes {
			if node.Name != "Download bytes" {
				continue
			}
			if node.Total != 12 || node.Completed > 12 {
				t.Fatal("incorrect byte count", node)
			}
			foundPartial = foundPartial || (node.Completed > 0 && node.Completed < 12)
			foundComplete = foundComplete || node.Completed == 12
		}
	}
	if !foundPartial || !foundComplete {
		t.Fatal("missing byte progress", foundPartial, foundComplete)
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 2 {
		t.Fatal("leaked or double-counted batches", nodes)
	}
	before := len(fetcher.snapshots)
	if _, err := FetchAll(r.Context, root, testConfig(), packages, fetcher, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.snapshots) != before {
		t.Fatal("cache hit fetched sources")
	}
	if _, err := FetchAll(r.Context, root, testConfig(), packages, fetcher, func(int) bool { return true }, nil); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.snapshots) == before {
		t.Fatal("refresh skipped source checks")
	}
}

func TestFailedDownloadEndsProgressAndPreservesCache(t *testing.T) {
	r := progresstest.New(t)
	p := testEntry("https://source.test/pkg")
	root := t.TempDir()
	name := filepath.Join(root, "sha256", p.SHA256)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("package-data"), 0600); err != nil {
		t.Fatal(err)
	}
	fetcher := &progressFetcher{t: t, recorder: r, bad: true}
	_, err := FetchAll(r.Context, root, testConfig(), []contract.Package{p}, fetcher, func(int) bool { return true }, nil)
	if err == nil {
		t.Fatal("accepted invalid source")
	}
	if err := VerifyFile(name, p); err != nil {
		t.Fatal("changed cache", err)
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 {
		t.Fatal("failed download leaked tasks", nodes)
	}
	files, err := os.ReadDir(filepath.Dir(name))
	if err != nil || len(files) != 1 {
		t.Fatal("temporary download leaked", files, err)
	}
}

func TestVerifyFileContextCanceled(t *testing.T) {
	r := progresstest.New(t)
	p := testEntry("https://source.test/pkg")
	name := filepath.Join(t.TempDir(), "pkg")
	if err := os.WriteFile(name, []byte("package-data"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(r.Context)
	cancel()
	if err := VerifyFileContext(ctx, name, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 {
		t.Fatal(nodes)
	}
}
