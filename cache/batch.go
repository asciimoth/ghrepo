package cache

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/internal/parallel"
	"github.com/asciimoth/ghrepo/internal/progress"
)

// DefaultDownloadWorkers limits concurrent package downloads.
const DefaultDownloadWorkers = 4

// FetchAll checks cached packages with local workers and limits downloads to
// DefaultDownloadWorkers. If refresh returns true for a package, its public
// source is checked even on a cache hit. notify is called when a download starts
// and ends; calls can come from
// different workers.
func FetchAll(ctx context.Context, root string, r contract.Repository, packages []contract.Package, fetcher Fetcher, refresh func(index int) bool, notify func(index int, done bool)) ([]string, error) {
	if err := (contract.Manifest{Schema: 1, Packages: packages}).Validate(r); err != nil {
		return nil, err
	}
	names := make([]string, len(packages))
	force := make([]bool, len(packages))
	for index := range packages {
		force[index] = refresh != nil && refresh(index)
	}
	if err := progress.Batch(ctx, "Check cache", len(packages), parallel.LocalWorkers(), func(i int) string { return packages[i].Path }, func(ctx context.Context, index int) error {
		p := packages[index]
		name := filepath.Join(root, "sha256", p.SHA256)
		if !force[index] && VerifyFileContext(ctx, name, p) == nil {
			names[index] = name
		}
		return nil
	}); err != nil {
		return nil, err
	}
	pending := make([]int, 0, len(packages))
	for index := range packages {
		if names[index] == "" {
			pending = append(pending, index)
		}
	}
	err := progress.Batch(ctx, "Fetch sources", len(pending), DefaultDownloadWorkers, func(i int) string { return packages[pending[i]].Path }, func(ctx context.Context, job int) error {
		index := pending[job]
		p := packages[index]
		if notify != nil {
			notify(index, false)
			defer notify(index, true)
		}
		var name string
		var err error
		if force[index] {
			name, err = FetchVerified(ctx, root, r, p, fetcher)
		} else {
			name, err = Ensure(ctx, root, r, p, fetcher)
		}
		if err != nil {
			return fmt.Errorf("package %s: %w", p.Path, err)
		}
		names[index] = name
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}
