package rpm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/ghrepo/internal/progresstest"
)

func TestBuildVerifyProgressAndFailureCleanup(t *testing.T) {
	root, key, cacheRoot, r, m, local := fixture(t)
	recorder := progresstest.New(t)
	out := filepath.Join(root, "progress-rpm")
	if err := BuildWithKeyRootContext(recorder.Context, out, cacheRoot, root, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := VerifyContext(recorder.Context, out, key, filepath.Join(root, "site"), r, m); err != nil {
		t.Fatal(err)
	}
	if nodes := recorder.Snapshot(t); len(nodes) != 1 || nodes[0].Completed != 2 {
		t.Fatal("build or verification tasks leaked", nodes)
	}
	ctx, cancel := context.WithCancel(recorder.Context)
	cancel()
	failed := filepath.Join(root, "canceled-rpm")
	if err := BuildWithKeyRootContext(ctx, failed, cacheRoot, root, r, m, local); err == nil {
		t.Fatal("canceled build succeeded")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("canceled build left output", err)
	}
	if err := VerifyContext(ctx, out, key, filepath.Join(root, "site"), r, m); err == nil {
		t.Fatal("canceled verification succeeded")
	}
	signed := filepath.Join(root, "canceled-sign.rpm")
	source := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
	if err := SignCopyContext(ctx, source, signed, local, []string{key}); err == nil {
		t.Fatal("canceled signing succeeded")
	}
	if _, err := os.Stat(signed); !os.IsNotExist(err) {
		t.Fatal("canceled signing left output", err)
	}
	if err := os.WriteFile(source, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BuildWithKeyRootContext(recorder.Context, failed, cacheRoot, root, r, m, local); err == nil {
		t.Fatal("invalid cache accepted")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("failed build left output", err)
	}
	if nodes := recorder.Snapshot(t); len(nodes) != 1 {
		t.Fatal("failure leaked tasks", nodes)
	}
}
