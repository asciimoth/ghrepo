package inspect

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFileValidation(t *testing.T) {
	raw, err := os.ReadFile("../contract/testdata/packages/hello_compat.deb")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	regular := filepath.Join(root, "package.deb")
	empty := filepath.Join(root, "empty.deb")
	invalid := filepath.Join(root, "invalid.deb")
	link := filepath.Join(root, "symlink.deb")
	for name, data := range map[string][]byte{regular: raw, empty: {}, invalid: []byte("not a package")} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		max        int64
	}{
		{"missing", filepath.Join(root, "missing"), 10000},
		{"directory", root, 10000},
		{"symlink", link, 10000},
		{"empty", empty, 10000},
		{"malformed", invalid, 10000},
		{"too large", regular, int64(len(raw) - 1)},
		{"zero limit", regular, 0},
		{"negative limit", regular, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, size, hash, err := File(tc.path, tc.max)
			if err == nil || size != 0 || hash != "" || m.Format != "" {
				t.Fatalf("File = %+v, %d, %q, %v", m, size, hash, err)
			}
		})
	}
	m, size, hash, err := File(regular, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if m.Identity.Name != "hello-compat" || size != int64(len(raw)) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("File = %+v, %d, %q", m, size, hash)
	}
}
