package inspect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixtures(t *testing.T) {
	root := "../contract/testdata/packages"
	for _, tc := range []struct{ name, format, identity string }{
		{"hello_1.2.0-1_amd64.deb", "deb", "hello"},
		{"empty-0.1-1.x86_64.rpm", "rpm", "empty"},
		{"payload-gzip.rpm", "rpm", "payload-test"},
		{"payload-xz.rpm", "rpm", "payload-test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, size, hash, err := File(filepath.Join(root, tc.name), 2147483647)
			if err != nil {
				t.Fatal(err)
			}
			if m.Format != tc.format || m.Identity.Name != tc.identity || size < 1 || len(hash) != 64 {
				t.Fatal(m, size, hash)
			}
		})
	}
	for _, name := range []string{"hello_malformed.deb", "hello_unsafe.deb"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Bytes(data); err == nil {
				t.Fatal("accepted invalid DEB")
			}
		})
	}
}
