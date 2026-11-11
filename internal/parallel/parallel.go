// Package parallel runs bounded batches of independent work.
package parallel

import (
	"context"
	"runtime"
	"sync"
)

// LocalWorkers allows more local work than downloads, while limiting open files
// and memory used by package inspection.
func LocalWorkers() int { return min(32, max(8, runtime.GOMAXPROCS(0))) }

// Do calls work once per index, with at most limit calls at a time. On error it
// cancels pending work and waits for running calls before returning. Work must
// keep writes to separate results or protect shared state.
func Do(ctx context.Context, count, limit int, work func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	jobs := make(chan int)
	for range min(count, max(1, limit)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := work(ctx, index); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
send:
	for index := range count {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
