package inspect

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func compatibilityTool(t *testing.T, tool, requiredEnv string) string {
	t.Helper()
	name, err := exec.LookPath(tool)
	if err != nil {
		if os.Getenv(requiredEnv) == "1" {
			t.Fatalf("%s is required when %s=1", tool, requiredEnv)
		}
		t.Skip(tool + " unavailable")
	}
	return name
}

func TestDEBDpkgCompatibility(t *testing.T) {
	tool := compatibilityTool(t, "dpkg-deb", "GHREPO_DEB_COMPAT")
	for _, tc := range controlFieldCases() {
		t.Run(tc.name, func(t *testing.T) {
			data := testDEBEntries(t, append(standardControlEntries("./"), regularEntry("./control", tc.control)))
			name := filepath.Join(t.TempDir(), "package.deb")
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			// Ask the independent parser first, including for extension fields.
			cmd := exec.Command(tool, "--field", name, tc.key)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("dpkg-deb rejected control syntax: %v: %s", err, stderr.String())
			}
			m := requireDEBMetadata(t, data)
			got, want := strings.TrimSpace(string(output)), strings.TrimSpace(m.Fields[tc.key])
			if tc.key == "Depends" {
				// dpkg unfolds dependency fields when it prints them.
				got = strings.Join(strings.Fields(got), " ")
				want = strings.Join(strings.Fields(want), " ")
			}
			if got != want {
				t.Fatalf("dpkg field %s = %q, inspector = %q", tc.key, got, want)
			}
		})
	}
	for _, format := range []tar.Format{tar.FormatUSTAR, tar.FormatGNU} {
		for _, prefix := range []string{"", "./"} {
			t.Run(fmt.Sprintf("archive/%s/prefix=%q", format, prefix), func(t *testing.T) {
				entries := append([]tarEntry{{header: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}}, regularEntry(prefix+"control", testControl)}, standardControlEntries(prefix)...)
				data := testDEB(t, testGzip(t, testTar(t, format, entries)), testGzip(t, testTar(t, format, nil)))
				name := filepath.Join(t.TempDir(), "package.deb")
				if err := os.WriteFile(name, data, 0600); err != nil {
					t.Fatal(err)
				}
				output, err := exec.Command(tool, "--show", "--showformat=${Package} ${Version} ${Architecture}", name).CombinedOutput()
				if err != nil || string(output) != "hello 1.2.0-1 amd64" {
					t.Fatalf("dpkg-deb: %v: %s", err, output)
				}
				requireDEBMetadata(t, data)
			})
		}
	}
}

// Build real packages with the system tool. Normal tests need no package tools;
// the container compatibility job makes this dependency mandatory.
func TestDEBDpkgBuildCompatibility(t *testing.T) {
	tool := compatibilityTool(t, "dpkg-deb", "GHREPO_DEB_COMPAT")
	for _, arch := range []string{"amd64", "arm64", "all"} {
		for _, level := range []string{"1", "6", "9"} {
			t.Run(arch+"/gzip="+level, func(t *testing.T) {
				root := t.TempDir()
				pkg := filepath.Join(root, "pkg")
				if err := os.MkdirAll(filepath.Join(pkg, "DEBIAN"), 0755); err != nil {
					t.Fatal(err)
				}
				control := strings.Replace(testControl, "Architecture: amd64", "Architecture: "+arch, 1)
				entries := append(standardControlEntries(""), regularEntry("control", control))
				for _, entry := range entries {
					if err := os.WriteFile(filepath.Join(pkg, "DEBIAN", entry.header.Name), []byte(entry.body), os.FileMode(entry.header.Mode)); err != nil {
						t.Fatal(err)
					}
				}
				payload := filepath.Join(pkg, "usr", "share", "hello")
				if err := os.MkdirAll(payload, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(payload, "message"), []byte("hello\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("message", filepath.Join(payload, "link")); err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(root, "result.deb")
				cmd := exec.Command(tool, "--build", "--root-owner-group", "--uniform-compression", "-Zgzip", "-z"+level, pkg, name)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("dpkg-deb build: %v: %s", err, output)
				}
				m, _, _, err := File(name, 10*1024*1024)
				if err != nil {
					t.Fatal(err)
				}
				if m.Identity.Name != "hello" || m.Identity.Version != "1.2.0-1" || m.Identity.Architecture != arch {
					t.Fatalf("identity = %+v", m.Identity)
				}
			})
		}
	}
}

func TestRPMToolCompatibility(t *testing.T) {
	tool := compatibilityTool(t, "rpm", "GHREPO_RPM_COMPAT")
	for _, fixture := range []string{"empty-0.1-1.x86_64.rpm", "payload-gzip.rpm", "payload-xz.rpm", "compat.rpm"} {
		t.Run(fixture, func(t *testing.T) {
			name := filepath.Join("../contract/testdata/packages", fixture)
			m, _, _, err := File(name, 10*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			query := "%{NAME}\n%{EPOCHNUM}\n%{VERSION}\n%{RELEASE}\n%{ARCH}\n%{SUMMARY}\n%{LICENSE}\n"
			cmd := exec.Command(tool, "--query", "--package", "--queryformat", query, name)
			cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C")
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Join([]string{m.Identity.Name, m.Identity.Epoch, m.Identity.Version, m.Identity.Release, m.Identity.Architecture, m.Fields["Summary"], m.Fields["License"], ""}, "\n")
			if string(output) != want {
				t.Fatalf("rpm metadata = %q, inspector = %q", output, want)
			}
			query = "[%{REQUIRENAME}\t%{REQUIREVERSION}\t%{REQUIREFLAGS}\n]"
			output, err = exec.Command(tool, "--query", "--package", "--queryformat", query, name).Output()
			if err != nil {
				t.Fatal(err)
			}
			var deps strings.Builder
			for _, d := range m.Requirements {
				fmt.Fprintf(&deps, "%s\t%s\t%d\n", d.Name, d.Version, d.Flags)
			}
			if string(output) != deps.String() {
				t.Fatalf("rpm dependencies = %q, inspector = %q", output, deps.String())
			}
		})
	}
}

func TestRPMBuildCompatibility(t *testing.T) {
	tool := compatibilityTool(t, "rpmbuild", "GHREPO_RPM_BUILD_COMPAT")
	for _, compression := range []string{"w9.gzdio", "w6.xzdio", "w19.zstdio"} {
		t.Run(compression, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(tool, "-bb", "--define", "_topdir "+root,
				"--define", "_buildhost fixture.invalid", "--define", "_binary_payload "+compression,
				"--define", "use_source_date_epoch_as_buildtime 1", "--define", "clamp_mtime_to_source_date_epoch 1",
				"../contract/testdata/compat.spec")
			cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1", "LC_ALL=C", "TZ=UTC")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rpmbuild: %v: %s", err, output)
			}
			name := filepath.Join(root, "RPMS", "noarch", "ghrepo-compat-1.2-3.noarch.rpm")
			m, _, _, err := File(name, 10*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			if m.Identity.Name != "ghrepo-compat" || m.Identity.Epoch != "2" || m.Identity.Architecture != "noarch" || m.Fields["Summary"] != "Compatibility summary" || m.Fields["Description"] != "Compatibility description." {
				t.Fatalf("metadata = %+v", m)
			}
		})
	}
}

func TestDEBExtensionCompatibility(t *testing.T) {
	tool := compatibilityTool(t, "dpkg-deb", "GHREPO_DEB_COMPAT")
	for pos := uint8(0); pos < 3; pos++ {
		t.Run(fmt.Sprint(pos), func(t *testing.T) {
			raw := debWithExtensions(t, "_gpgorigin", []byte("opaque signature"), pos)
			file := filepath.Join(t.TempDir(), "package.deb")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(tool, "--field", file, "Package").CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "hello" {
				t.Fatalf("dpkg-deb: %v: %s", err, out)
			}
			// --contents reads past control to the payload and any intervening
			// extension members.
			if out, err := exec.Command(tool, "--contents", file).CombinedOutput(); err != nil {
				t.Fatalf("dpkg-deb payload: %v: %s", err, out)
			}
			requireDEBMetadata(t, raw)
		})
	}
}
