package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/cache"
)

// The worker uses the real Runner and offline fixtures in a separate process so
// zpg owns the upstream pipe exactly as it does for the installed CLI.
func TestZPGWorker(t *testing.T) {
	if os.Getenv("GHREPO_ZPG_WORKER") != "1" {
		return
	}
	fetcher := fixtureFetcher{map[string][]byte{
		"https://example.com/hello.deb": mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb"),
		"https://example.com/empty.rpm": mustRead(t, "../contract/testdata/packages/empty-0.1-1.x86_64.rpm"),
	}}
	var f cache.Fetcher = slowProgressFetcher{fetcher}
	if os.Getenv("GHREPO_ZPG_FAIL") == "1" {
		f = fixtureFetcher{map[string][]byte{}}
	}
	runner := Runner{Dir: os.Getenv("GHREPO_ZPG_REPO"), Fetcher: f}
	args := strings.Split(os.Getenv("GHREPO_ZPG_ARGS"), " ")
	if err := runner.Run(context.Background(), args); err != nil {
		fmt.Fprintln(os.Stderr, "ghrepo:", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, "worker stdout preserved")
	fmt.Fprintln(os.Stderr, "worker stderr preserved")
	os.Exit(0)
}

type slowProgressFetcher struct{ base cache.Fetcher }

func (f slowProgressFetcher) Fetch(ctx context.Context, url string, sources, redirects []string) (io.ReadCloser, error) {
	body, err := f.base.Fetch(ctx, url, sources, redirects)
	if err != nil {
		return nil, err
	}
	return &slowProgressBody{ReadCloser: body, ctx: ctx}, nil
}

type slowProgressBody struct {
	io.ReadCloser
	ctx context.Context
}

func (r *slowProgressBody) Read(p []byte) (int, error) {
	select {
	case <-time.After(100 * time.Millisecond):
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
	return r.ReadCloser.Read(p[:min(256, len(p))])
}

func TestZPGFlows(t *testing.T) {
	zpg, err := exec.LookPath("zpg")
	if err != nil {
		if os.Getenv("GHREPO_REQUIRE_ZPG") == "1" {
			t.Fatal("zpg is required; run in nix develop")
		}
		t.Skip("zpg unavailable; use just test-progress in nix develop")
	}
	installSlowProgressTools(t, "0.15")
	runner, dir := prepareFlow(t)
	command(t, dir, "git", "config", "user.name", "Progress Test")
	command(t, dir, "git", "config", "user.email", "progress@example.org")
	for _, tc := range []struct {
		args string
		fail bool
	}{
		{"sync", false}, {"rebuild", false}, {"check --refresh-sources", false},
		{"site-build", false}, {"publish --dry-run", false}, {"check --refresh-sources", true},
	} {
		t.Run(fmt.Sprintf("%s/fail=%v", tc.args, tc.fail), func(t *testing.T) {
			if tc.args == "sync" {
				_, _, root := runner.paths()
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(t.TempDir(), "progress.log")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, zpg, "--test", "--test-output", log, os.Args[0], "-test.run=^TestZPGWorker$")
			cmd.Env = append(os.Environ(), "GHREPO_ZPG_WORKER=1", "GHREPO_ZPG_REPO="+dir, "GHREPO_ZPG_ARGS="+tc.args)
			if tc.fail {
				cmd.Env = append(cmd.Env, "GHREPO_ZPG_FAIL=1")
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("command result: %v\n%s", err, out)
			}
			if !tc.fail && (!strings.Contains(string(out), "worker stdout preserved") || !strings.Contains(string(out), "worker stderr preserved")) {
				t.Fatalf("child output changed: %s", out)
			}
			data := string(mustRead(t, log))
			for _, bad := range []string{"error:", "no progress received", "incomplete"} {
				if strings.Contains(data, bad) {
					t.Fatalf("invalid protocol capture: %s", data)
				}
			}
			name := "ghrepo " + strings.Fields(tc.args)[0]
			if tc.fail {
				name += " (failed)"
			}
			if !strings.Contains(data, `name="`+name+`"`) {
				t.Fatalf("missing root %q: %s", name, data)
			}
			if !strings.Contains(data, "progress pipe closed; snapshots=") {
				t.Fatalf("pipe not closed: %s", data)
			}
			if !tc.fail && tc.args != "rebuild" {
				if !strings.Contains(data, `name="Download bytes"`) {
					t.Fatalf("missing byte task: %s", data)
				}
			}
			if !tc.fail && tc.args != "sync" {
				stages := []string{"Verify APT metadata", "Verify RPM metadata"}
				if tc.args == "rebuild" || tc.args == "publish --dry-run" {
					stages = append(stages, "Build APT metadata", "Build RPM metadata")
				}
				if strings.HasPrefix(tc.args, "check") || tc.args == "site-build" || tc.args == "publish --dry-run" {
					stages = append(stages, "Build site")
				}
				for _, stage := range stages {
					if !strings.Contains(data, `name="`+stage+`"`) {
						t.Fatalf("missing stage %q: %s", stage, data)
					}
				}
			}
			t.Logf("validated %s with zpg --test (%d log bytes)", tc.args, len(data))
		})
	}
}

// TestProgressDemo runs only from just demo-progress. Its signed repository and
// downloaded packages stay in a test-owned temporary directory.
func TestProgressDemo(t *testing.T) {
	if os.Getenv("GHREPO_PROGRESS_DEMO") != "1" {
		t.Skip("run just demo-progress to view the live progress tree")
	}
	installSlowProgressTools(t, "0.30")
	runner, _ := prepareFlow(t)
	runner.Fetcher = slowProgressFetcher{runner.Fetcher}
	_, _, cacheRoot := runner.paths()
	if err := os.RemoveAll(cacheRoot); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"sync"}, {"rebuild"}, {"check", "--refresh-sources"}, {"site-build"},
	} {
		t.Logf("Running ghrepo %s", strings.Join(args, " "))
		if err := runner.Run(context.Background(), args); err != nil {
			t.Fatalf("ghrepo %s: %v", strings.Join(args, " "), err)
		}
	}
}

// The wrappers make real GPG work visible for several progress updates. They
// also check that a script passes its child progress descriptor through exec.
func installSlowProgressTools(t *testing.T, delay string) {
	t.Helper()
	wrappers := t.TempDir()
	for _, tool := range []string{"gpg", "gpgv"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Skip(tool + " unavailable")
		}
		script := "#!/bin/sh\nsleep " + delay + "\nexec '" + strings.ReplaceAll(real, "'", "'\"'\"'") + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(wrappers, tool), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", wrappers+string(os.PathListSeparator)+os.Getenv("PATH"))
}
