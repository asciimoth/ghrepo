package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/asciimoth/ghrepo/contract"
)

// DefaultDownloadWorkers limits concurrent package downloads.
const DefaultDownloadWorkers = 4

// FetchAll verifies packages with a bounded number of workers. If refresh
// returns true for a package, its public source is checked even on a cache hit.
// Progress is called when a download starts and ends; calls can come from
// different workers.
func FetchAll(ctx context.Context, root string, r contract.Repository, packages []contract.Package, fetcher Fetcher, refresh func(index int) bool, progress func(index int, done bool)) ([]string, error) {
	if err := (contract.Manifest{Schema: 1, Packages: packages}).Validate(r); err != nil {
		return nil, err
	}
	names := make([]string, len(packages))
	if len(packages) == 0 {
		return names, ctx.Err()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	var firstError error
	var errorOnce sync.Once
	count := min(DefaultDownloadWorkers, len(packages))
	for range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if workerCtx.Err() != nil {
					return
				}
				p := packages[index]
				force := refresh != nil && refresh(index)
				name := filepath.Join(root, "sha256", p.SHA256)
				if !force && VerifyFile(name, p) == nil {
					names[index] = name
					continue
				}
				if progress != nil {
					progress(index, false)
				}
				var err error
				if force {
					name, err = FetchVerified(workerCtx, root, r, p, fetcher)
				} else {
					name, err = Ensure(workerCtx, root, r, p, fetcher)
				}
				if progress != nil {
					progress(index, true)
				}
				if err != nil {
					errorOnce.Do(func() {
						firstError = fmt.Errorf("package %s: %w", p.Path, err)
						cancel()
					})
					return
				}
				names[index] = name
			}
		}()
	}
send:
	for index := range packages {
		select {
		case jobs <- index:
		case <-workerCtx.Done():
			break send
		}
	}
	close(jobs)
	workers.Wait()
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return names, nil
}
