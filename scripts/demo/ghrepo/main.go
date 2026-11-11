// Command ghrepo runs the real CLI with slow, offline package sources for VHS.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asciimoth/ghrepo/cmd"
	"github.com/asciimoth/ghrepo/contract"
)

const release = "https://github.com/asciimoth/demo/releases/download/"

var packages = map[string]string{
	release + "v1/hello_1.2.0-1_amd64.deb": "hello_1.2.0-1_amd64.deb",
	release + "v1/empty-0.1-1.x86_64.rpm":  "empty-0.1-1.x86_64.rpm",
	release + "v2/hello_2.0.0-1_amd64.deb": "hello_2.0.0-1_amd64.deb",
}

type fixtureFetcher struct{ root string }

type metadataTransport struct{}

func (metadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != "https://api.github.com/repos/asciimoth/demo" {
		return nil, errors.New("unknown demo metadata URL")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"description":"Example CLI packages","topics":["cli","tools"]}`)),
		Header:     make(http.Header),
	}, nil
}

func (f fixtureFetcher) Fetch(ctx context.Context, raw string, sources, _ []string) (io.ReadCloser, error) {
	if err := contract.ValidateSourceURL(raw, sources); err != nil {
		return nil, err
	}
	name, ok := packages[raw]
	if !ok {
		return nil, errors.New("unknown demo package URL")
	}
	file, err := os.Open(filepath.Join(f.root, name))
	if err != nil {
		return nil, err
	}
	return &slowReader{ReadCloser: file, ctx: ctx}, nil
}

type slowReader struct {
	io.ReadCloser
	ctx context.Context
}

func (r *slowReader) Read(p []byte) (int, error) {
	select {
	case <-time.After(35 * time.Millisecond):
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
	return r.ReadCloser.Read(p[:min(len(p), 64)])
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := os.Getenv("GHREPO_DEMO_FIXTURES")
	if root == "" {
		fmt.Fprintln(os.Stderr, "ghrepo demo: GHREPO_DEMO_FIXTURES is required")
		os.Exit(1)
	}
	runner := cmd.Runner{
		Fetcher:      fixtureFetcher{root: root},
		GitHubClient: &http.Client{Transport: metadataTransport{}},
	}
	if err := runner.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ghrepo:", err)
		os.Exit(1)
	}
}
