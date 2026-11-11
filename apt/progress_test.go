package apt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/ghrepo/internal/progresstest"
)

func TestBuildVerifyProgressAndFailureCleanup(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixture(t)
	recorder := progresstest.New(t)
	out := filepath.Join(root, "progress-deb")
	if err := BuildContext(recorder.Context, out, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := VerifyContext(recorder.Context, out, key, r, m); err != nil {
		t.Fatal(err)
	}
	if nodes := recorder.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 2 {
		t.Fatal("build or verification tasks leaked", nodes)
	}
	ctx, cancel := context.WithCancel(recorder.Context)
	cancel()
	failed := filepath.Join(root, "canceled-deb")
	if err := BuildContext(ctx, failed, cacheRoot, r, m, local); err == nil {
		t.Fatal("canceled build succeeded")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("canceled build left output", err)
	}
	if err := VerifyContext(ctx, out, key, r, m); err == nil {
		t.Fatal("canceled verification succeeded")
	}
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BuildContext(recorder.Context, failed, cacheRoot, r, m, local); err == nil {
		t.Fatal("invalid cache accepted")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("failed build left output", err)
	}
	if nodes := recorder.Snapshot(t); len(nodes) != 1 {
		t.Fatal("failure leaked tasks", nodes)
	}
}
