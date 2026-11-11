// Package progress carries a Zig progress task through an operation's context.
// Reporting is optional and never changes command output or operation results.
package progress

import (
	"context"
	"io"

	"github.com/asciimoth/ghrepo/internal/parallel"
	"github.com/asciimoth/zpgo"
)

type nodeKey struct{}

// WithNode attaches a task without creating another reporter on the same pipe.
func WithNode(ctx context.Context, node zpgo.Node) context.Context {
	return context.WithValue(ctx, nodeKey{}, node)
}

// Node returns the current task, or a disabled task when reporting is absent.
func Node(ctx context.Context) zpgo.Node {
	node, _ := ctx.Value(nodeKey{}).(zpgo.Node)
	return node
}

// Start creates a child task. End it only after its workers and processes stop.
func Start(ctx context.Context, name string, total uint64) (context.Context, zpgo.Node) {
	node := Node(ctx).Start(name, total)
	return WithNode(ctx, node), node
}

// Batch reports a bounded batch. Only running items use task slots. End counts
// each item once; it means the work ended, not that it succeeded.
func Batch(ctx context.Context, name string, count, limit int, label func(int) string, work func(context.Context, int) error) error {
	ctx, batch := Start(ctx, name, uint64(count))
	defer batch.End()
	return parallel.Do(ctx, count, limit, func(ctx context.Context, index int) error {
		ctx, item := Start(ctx, label(index), 0)
		defer item.End()
		return work(ctx, index)
	})
}

type reader struct {
	ctx    context.Context
	source io.Reader
	node   zpgo.Node
}

// Reader counts bytes read and checks cancellation between reads. Use a separate
// task with a byte total, never a task whose children also complete items.
func Reader(ctx context.Context, source io.Reader, node zpgo.Node) io.Reader {
	return &reader{ctx: ctx, source: source, node: node}
}

func (r *reader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	r.node.Add(uint64(n))
	return n, err
}

// Begin creates one reporter for a command, or nests under an existing reporter.
// The returned function marks failures and closes only resources it owns.
func Begin(ctx context.Context, name string) (context.Context, func(error)) {
	if _, ok := ctx.Value(nodeKey{}).(zpgo.Node); ok {
		childCtx, task := Start(ctx, name, 0)
		return childCtx, func(_ error) { task.End() }
	}
	reporter, err := zpgo.New(zpgo.Options{RootName: name})
	if err != nil {
		return ctx, func(error) {}
	}
	return WithNode(ctx, reporter.Root()), func(err error) {
		if err != nil {
			reporter.Root().SetName(name + " (failed)")
		}
		// A closed or full progress pipe must not change the command result.
		_ = reporter.Close()
	}
}
