package progress_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/internal/progress"
	"github.com/asciimoth/ghrepo/internal/progresstest"
)

func TestTaskLifecycleAndReaderCounts(t *testing.T) {
	r := progresstest.New(t)
	ctx, finish := progress.Begin(r.Context, "command")
	ctx, stage := progress.Start(ctx, "stage", 2)
	_, task := progress.Start(ctx, "bytes", 6)
	reader := progress.Reader(ctx, strings.NewReader("abcdef"), task)
	buf := make([]byte, 3)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	nodes := r.Snapshot(t)
	n := progresstest.Find(t, nodes, "bytes")
	if n.Completed != 3 || n.Total != 6 {
		t.Fatalf("byte counts: %+v", n)
	}
	if n := progresstest.Find(t, nodes, "stage"); n.Completed != 0 {
		t.Fatal("bytes counted as child items")
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	if n := progresstest.Find(t, r.Snapshot(t), "bytes"); n.Completed != 6 {
		t.Fatal(n)
	}
	task.End()
	task.End()
	if n := progresstest.Find(t, r.Snapshot(t), "stage"); n.Completed != 1 {
		t.Fatal("child counted more than once", n)
	}
	stage.End()
	finish(nil)
	if nodes := r.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 1 {
		t.Fatal(nodes)
	}
	// Nested Begin must not close the caller's reporter.
	_, stillOpen := progress.Start(r.Context, "still open", 0)
	defer stillOpen.End()
	progresstest.Find(t, r.Snapshot(t), "still open")
}

func TestBatchReportsOnlyActiveWorkers(t *testing.T) {
	r := progresstest.New(t)
	ctx, cancel := context.WithTimeout(r.Context, 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 300)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- progress.Batch(ctx, "packages", 300, 8, func(i int) string { return fmt.Sprintf("package %d", i) }, func(ctx context.Context, _ int) error {
			started <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	for range 8 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	nodes := r.Snapshot(t)
	if len(nodes) != 10 {
		t.Fatalf("expected root, batch, eight workers: %+v", nodes)
	}
	batch := progresstest.Find(t, nodes, "packages")
	if batch.Total != 300 || batch.Completed != 0 {
		t.Fatal(batch)
	}
	for _, node := range nodes[2:] {
		if node.Parent != 1 {
			t.Fatal("wrong worker parent", node)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 1 {
		t.Fatal(nodes)
	}
}

func TestBatchErrorAndCancellationRemoveTasks(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			r := progresstest.New(t)
			ctx, cancel := context.WithCancel(r.Context)
			defer cancel()
			want := errors.New("failed")
			if canceled {
				cancel()
				want = context.Canceled
			}
			err := progress.Batch(ctx, "failed batch", 10, 2, func(int) string { return "package" }, func(context.Context, int) error { return want })
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			if nodes := r.Snapshot(t); len(nodes) != 1 {
				t.Fatal("leaked tasks", nodes)
			}
		})
	}
}

func TestEmptyBatchAndDisabledReporting(t *testing.T) {
	for _, value := range []string{"", "not-a-descriptor", "65535"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ZIG_PROGRESS", value)
			ctx, finish := progress.Begin(context.Background(), "disabled")
			defer finish(nil)
			calls := 0
			for _, count := range []int{0, 3} {
				if err := progress.Batch(ctx, "batch", count, 1, func(int) string { return "item" }, func(context.Context, int) error { calls++; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 3 {
				t.Fatal(calls)
			}
		})
	}
}

func TestReaderCancellationAndReadError(t *testing.T) {
	r := progresstest.New(t)
	ctx, cancel := context.WithCancel(r.Context)
	_, task := progress.Start(ctx, "bytes", 3)
	defer task.End()
	want := errors.New("read failed")
	reader := progress.Reader(ctx, errorReader{want}, task)
	if n, err := reader.Read(make([]byte, 3)); n != 2 || !errors.Is(err, want) {
		t.Fatalf("%d %v", n, err)
	}
	cancel()
	if n, err := reader.Read(make([]byte, 3)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("%d %v", n, err)
	}
	if n := progresstest.Find(t, r.Snapshot(t), "bytes"); n.Completed != 2 {
		t.Fatal(n)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read(p []byte) (int, error) { copy(p, "ab"); return 2, r.err }

func TestBatchCountsEachFinishedPackageOnce(t *testing.T) {
	r := progresstest.New(t)
	ctx, cancel := context.WithTimeout(r.Context, 5*time.Second)
	defer cancel()
	ready := make(chan int)
	next := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- progress.Batch(ctx, "batch", 3, 1, func(int) string { return "package" }, func(ctx context.Context, index int) error {
			select {
			case ready <- index:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-next:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	for index := range 3 {
		select {
		case got := <-ready:
			if got != index {
				t.Fatal(got)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		node := progresstest.Find(t, r.Snapshot(t), "batch")
		if node.Total != 3 || node.Completed != uint32(index) {
			t.Fatal("wrong completed count", node)
		}
		next <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
