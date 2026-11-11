// Package progresstest decodes progress packets independently for integration tests.
package progresstest

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/internal/progress"
	"github.com/asciimoth/zpgo"
)

type Node struct {
	Name             string
	Completed, Total uint32
	Parent           byte
}

type Recorder struct {
	Reporter *zpgo.Progress
	Context  context.Context
	mu       sync.Mutex
	packets  chan []Node
	errors   chan error
}

func New(t *testing.T) *Recorder {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p, err := zpgo.New(zpgo.Options{File: w, Manual: true, RootName: "test"})
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		t.Fatal(err)
	}
	c := &Recorder{Reporter: p, Context: progress.WithNode(context.Background(), p.Root()), packets: make(chan []Node, 64), errors: make(chan error, 1)}
	go func() {
		for {
			nodes, err := Read(r)
			if err != nil {
				c.errors <- err
				return
			}
			c.packets <- nodes
		}
	}()
	t.Cleanup(func() { _ = p.Close(); _ = r.Close() })
	return c
}

// Read checks the 120-byte-name wire format, including the parent tree.
func Read(r io.Reader) ([]Node, error) {
	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return nil, err
	}
	if count[0] >= 254 {
		return nil, errors.New("reserved node count")
	}
	const recordSize = 128
	n := int(count[0])
	data := make([]byte, n*(recordSize+1))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	nodes := make([]Node, n)
	for i := range nodes {
		record := data[i*recordSize : (i+1)*recordSize]
		nodes[i] = Node{Name: strings.TrimRight(string(record[8:]), "\x00"), Completed: binary.LittleEndian.Uint32(record), Total: binary.LittleEndian.Uint32(record[4:]), Parent: data[n*recordSize+i]}
		if nodes[i].Total == ^uint32(0) {
			return nil, errors.New("reserved total")
		}
		if i == 0 && nodes[i].Parent != 255 {
			return nil, errors.New("invalid root parent")
		}
		if i > 0 && int(nodes[i].Parent) >= i {
			return nil, errors.New("invalid parent order")
		}
	}
	return nodes, nil
}

func (c *Recorder) Snapshot(t *testing.T) []Node {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := c.Reporter.Flush()
		if err == nil {
			break
		}
		if !errors.Is(err, zpgo.ErrWouldBlock) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case nodes := <-c.packets:
		return nodes
	case err := <-c.errors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no progress packet")
	}
	return nil
}

func Find(t *testing.T, nodes []Node, name string) Node {
	t.Helper()
	for _, node := range nodes {
		if node.Name == name {
			return node
		}
	}
	t.Fatalf("missing task %q in %+v", name, nodes)
	return Node{}
}
