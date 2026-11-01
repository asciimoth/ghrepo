package inspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

func TestPackageArchitectureAndServedPath(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64", "i386", "armhf", "riscv64gc"} {
		t.Run("deb/"+arch, func(t *testing.T) {
			control := strings.Replace(testControl, "Architecture: amd64", "Architecture: "+arch, 1)
			data := testDEBEntries(t, []tarEntry{regularEntry("control", control)})
			checkPackageArchitectureAndPath(t, data, arch, "deb/pool/hello_1.2.0-1_"+arch+"_000000000000.deb")
		})
	}
	for _, arch := range []string{"x86_64", "aarch64", "i386", "armv6hl", "riscv64gc"} {
		t.Run("rpm/"+arch, func(t *testing.T) {
			data := testRPM(map[uint32]*rpmTestTag{
				rpmutils.ARCH: rpmStrings(rpmutils.ARCH, rpmutils.RPM_STRING_TYPE, arch),
			})
			checkPackageArchitectureAndPath(t, data, arch, "rpm/packages/fixture-0_1.2-3."+arch+".000000000000.rpm")
		})
	}
}

func checkPackageArchitectureAndPath(t *testing.T, data []byte, arch, wantPath string) {
	t.Helper()
	meta, err := Bytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Identity.Architecture != arch {
		t.Fatalf("architecture = %q, want %q", meta.Identity.Architecture, arch)
	}
	p := contract.Package{Format: meta.Format, Identity: meta.Identity, SHA256: strings.Repeat("0", 64)}
	path, err := contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath {
		t.Fatalf("served path = %q, want %q", path, wantPath)
	}
}

func TestFixtures(t *testing.T) {
	root := "../contract/testdata/packages"
	for _, tc := range []struct{ name, format, identity string }{
		{"hello_1.2.0-1_amd64.deb", "deb", "hello"},
		{"hello_compat.deb", "deb", "hello-compat"},
		{"empty-0.1-1.x86_64.rpm", "rpm", "empty"},
		{"payload-gzip.rpm", "rpm", "payload-test"},
		{"payload-xz.rpm", "rpm", "payload-test"},
		{"compat.rpm", "rpm", "ghrepo-compat"},
		{"eddsa-sha512.rpm", "rpm", "empty"},
		{"hello_signed-envelope.deb", "deb", "hello"},
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
