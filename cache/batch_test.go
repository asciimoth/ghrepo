package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/contract"
)

type gatedFetcher struct {
	mu      sync.Mutex
	active  int
	maximum int
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *gatedFetcher) Fetch(ctx context.Context, raw string, _, _ []string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.active++
	f.calls++
	if f.active > f.maximum {
		f.maximum = f.active
	}
	f.mu.Unlock()
	f.started <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return io.NopCloser(strings.NewReader(strings.TrimPrefix(raw, "https://source.test/"))), nil
}

func TestFetchAllLimitsConcurrentDownloads(t *testing.T) {
	packages := make([]contract.Package, 7)
	for i := range packages {
		data := fmt.Sprintf("package-%d", i)
		p := testEntry("https://source.test/" + data)
		p.Identity.Name = data
		p.Size = int64(len(data))
		sum := sha256.Sum256([]byte(data))
		p.SHA256 = hex.EncodeToString(sum[:])
		p.Path, _ = contract.ServedPath(p)
		packages[i] = p
	}
	fetcher := &gatedFetcher{started: make(chan struct{}, len(packages)), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(fetcher.release) }) }
	defer release()
	root := t.TempDir()
	result := make(chan error, 1)
	go func() {
		_, err := FetchAll(context.Background(), root, testConfig(), packages, fetcher, nil, nil)
		result <- err
	}()
	for range DefaultDownloadWorkers {
		select {
		case <-fetcher.started:
		case err := <-result:
			t.Fatalf("downloads stopped before all workers started: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("downloads did not start concurrently")
		}
	}
	fetcher.mu.Lock()
	active := fetcher.active
	fetcher.mu.Unlock()
	if active != DefaultDownloadWorkers {
		t.Fatalf("active downloads: got %d, want %d", active, DefaultDownloadWorkers)
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("downloads did not finish")
	}
	fetcher.mu.Lock()
	maximum, calls := fetcher.maximum, fetcher.calls
	fetcher.mu.Unlock()
	if maximum != DefaultDownloadWorkers || calls != len(packages) {
		t.Fatalf("downloads: maximum %d, calls %d", maximum, calls)
	}
	if _, err := FetchAll(context.Background(), root, testConfig(), packages, fetcher, nil, nil); err != nil {
		t.Fatal(err)
	}
	fetcher.mu.Lock()
	calls = fetcher.calls
	fetcher.mu.Unlock()
	if calls != len(packages) {
		t.Fatalf("cached packages caused %d extra fetches", calls-len(packages))
	}
}
