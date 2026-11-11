package parallel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoBoundedAndComplete(t *testing.T) {
	const limit, count = 8, 23
	started := make(chan struct{}, count)
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var active, maximum atomic.Int32
	results := make([]int, count)
	done := make(chan error, 1)
	go func() {
		done <- Do(ctx, count, limit, func(ctx context.Context, index int) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old; old = maximum.Load() {
				if maximum.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			results[index] = index + 1
			return nil
		})
	}()
	for range limit {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != limit || active.Load() != 0 {
		t.Fatal("incorrect worker limit or unfinished workers")
	}
	for index, value := range results {
		if value != index+1 {
			t.Fatalf("missing result %d", index)
		}
	}
}

func TestDoErrorCancelsAndWaits(t *testing.T) {
	want := errors.New("work failed")
	started := make(chan struct{})
	var finished atomic.Bool
	err := Do(context.Background(), 20, 2, func(ctx context.Context, index int) error {
		switch index {
		case 0:
			<-started
			return want
		case 1:
			close(started)
			<-ctx.Done()
			finished.Store(true)
			return ctx.Err()
		default:
			t.Error("started work after failure")
			return nil
		}
	})
	if !errors.Is(err, want) || !finished.Load() {
		t.Fatalf("error %v, finished %v", err, finished.Load())
	}
}

func TestDoCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, count := range []int{0, 10} {
		err := Do(ctx, count, 4, func(context.Context, int) error { t.Error("called canceled work"); return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	}
}
