package progress_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/internal/progress"
	"github.com/asciimoth/ghrepo/internal/progresstest"
	"github.com/asciimoth/zpgo"
)

func TestProgressWorker(t *testing.T) {
	if os.Getenv("GHREPO_PROGRESS_HELPER") != "1" {
		return
	}
	p, err := zpgo.New(zpgo.Options{RootName: "child worker", Manual: true})
	if err != nil {
		panic(err)
	}
	node := p.Root().Start("child work", 5)
	node.Add(2)
	if err := p.Flush(); err != nil {
		panic(err)
	}
	file := os.NewFile(3, "extra input")
	data, err := io.ReadAll(file)
	if err != nil {
		panic(err)
	}
	_ = file.Close()
	cwd, _ := os.Getwd()
	fmt.Printf("ready:%s:%s:%s\n", data, cwd, os.Getenv("ZIG_PROGRESS"))
	fmt.Fprintln(os.Stderr, "child stderr")
	var token [1]byte
	if _, err := io.ReadFull(os.Stdin, token[:]); err != nil {
		panic(err)
	}
	node.End()
	_ = p.Close()
	os.Exit(0)
}

func TestSpawnReportsChildThroughScriptAndPreservesAttributes(t *testing.T) {
	r := progresstest.New(t)
	ctx, cancel := context.WithTimeout(r.Context, 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	name := filepath.Join(dir, "extra")
	if err := os.WriteFile(name, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	extra, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	// A script must inherit its private child pipe through exec.
	cmd := exec.CommandContext(ctx, "sh", "-c", `exec "$@"`, "worker-script", os.Args[0], "-test.run=^TestProgressWorker$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GHREPO_PROGRESS_HELPER=1", "ZIG_PROGRESS=65535")
	cmd.ExtraFiles = []*os.File{extra}
	originalEnv := append([]string(nil), cmd.Env...)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- progress.Run(ctx, cmd) }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "ready:payload:"+dir+":4\n" {
		t.Fatalf("unexpected child attributes: %q", line)
	}
	nodes := r.Snapshot(t)
	worker := progresstest.Find(t, nodes, "child worker")
	work := progresstest.Find(t, nodes, "child work")
	if worker.Parent != 0 || work.Parent != 1 || work.Completed != 2 || work.Total != 5 {
		t.Fatal(nodes)
	}
	if _, err := input.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "child stderr\n" {
		t.Fatalf("stderr changed: %q", stderr.String())
	}
	if !reflect.DeepEqual(cmd.Env, originalEnv) || len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != extra {
		t.Fatal("command attributes changed")
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 1 {
		t.Fatal("child task not ended", nodes)
	}
}

func TestProcessOutputAndErrors(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			ctx := context.Background()
			if enabled {
				ctx = progresstest.New(t).Context
			}
			cmd := exec.CommandContext(ctx, "sh", "-c", "printf stdout; printf stderr >&2; exit 7")
			out, err := progress.Output(ctx, cmd)
			var exitErr *exec.ExitError
			if string(out) != "stdout" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || string(exitErr.Stderr) != "stderr" {
				t.Fatalf("output %q, error %+v", out, err)
			}
			cmd = exec.CommandContext(ctx, "sh", "-c", "printf first; printf second >&2")
			out, err = progress.CombinedOutput(ctx, cmd)
			if err != nil || string(out) != "firstsecond" {
				t.Fatalf("%q %v", out, err)
			}
			var stderr bytes.Buffer
			cmd = exec.CommandContext(ctx, "sh", "-c", "printf error >&2")
			cmd.Stderr = &stderr
			if _, err := progress.Output(ctx, cmd); err != nil || stderr.String() != "error" {
				t.Fatalf("%q %v", stderr.String(), err)
			}
		})
	}
}

func TestDisabledProcessRemovesStaleChannel(t *testing.T) {
	t.Setenv("ZIG_PROGRESS", "65535")
	ctx, finish := progress.Begin(context.Background(), "disabled")
	defer finish(nil)
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "${ZIG_PROGRESS-unset}"`)
	out, err := progress.Output(ctx, cmd)
	if err != nil || string(out) != "unset" {
		t.Fatalf("%q %v", out, err)
	}
	if os.Getenv("ZIG_PROGRESS") != "65535" {
		t.Fatal("parent environment changed")
	}
}

func TestProcessStartFailureAndCancellation(t *testing.T) {
	r := progresstest.New(t)
	cmd := exec.CommandContext(r.Context, filepath.Join(t.TempDir(), "missing"))
	if err := progress.Run(r.Context, cmd); err == nil {
		t.Fatal("missing process succeeded")
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 {
		t.Fatal("start failure leaked task", nodes)
	}
	ctx, cancel := context.WithCancel(r.Context)
	defer cancel()
	cmd = exec.CommandContext(ctx, "sh", "-c", "printf ready; read value")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- progress.Run(ctx, cmd) }()
	var ready [5]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled child succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child was not reaped")
	}
	if cmd.ProcessState == nil {
		t.Fatal("missing process exit state")
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 {
		t.Fatal("canceled process leaked task", nodes)
	}
}

func TestOutputRejectsConfiguredStreams(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.Stdout = io.Discard
	if _, err := progress.Output(ctx, cmd); err == nil {
		t.Fatal("overwrote stdout")
	}
	if _, err := progress.CombinedOutput(ctx, cmd); err == nil {
		t.Fatal("overwrote stdout")
	}
	cmd.Stdout = nil
	cmd.Stderr = io.Discard
	if _, err := progress.CombinedOutput(ctx, cmd); err == nil {
		t.Fatal("overwrote stderr")
	}
	if cmd.Process != nil {
		t.Fatal("started invalid command")
	}
}

func TestBrokenProgressPipeDoesNotFailWork(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p, err := zpgo.New(zpgo.Options{File: write, Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	_ = read.Close()
	defer func() { _ = p.Close() }()
	if err := p.Flush(); err == nil {
		t.Fatal("expected disconnected receiver")
	}
	ctx := progress.WithNode(context.Background(), p.Root())
	out, err := progress.Output(ctx, exec.CommandContext(ctx, "sh", "-c", "printf ok"))
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestConcurrentChildProcessesHaveSeparatePipes(t *testing.T) {
	r := progresstest.New(t)
	ctx, cancel := context.WithTimeout(r.Context, 10*time.Second)
	defer cancel()
	var inputs []io.WriteCloser
	done := make(chan error, 4)
	for range 4 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProgressWorker$")
		cmd.Env = append(os.Environ(), "GHREPO_PROGRESS_HELPER=1")
		cmd.ExtraFiles = []*os.File{file}
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = input.Close() })
		inputs = append(inputs, input)
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() { done <- progress.Run(ctx, cmd) }()
		if _, err := bufio.NewReader(output).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	nodes := r.Snapshot(t)
	roots, items := 0, 0
	for _, node := range nodes {
		switch node.Name {
		case "child worker":
			roots++
			if node.Parent != 0 {
				t.Fatal("wrong child root", nodes)
			}
		case "child work":
			items++
			if nodes[node.Parent].Name != "child worker" {
				t.Fatal("wrong descendant parent", nodes)
			}
		}
	}
	if roots != 4 || items != 4 {
		t.Fatal("child trees overwritten", nodes)
	}
	for _, input := range inputs {
		if _, err := input.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	for range inputs {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if nodes := r.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 4 {
		t.Fatal("child tasks not completed once", nodes)
	}
}
