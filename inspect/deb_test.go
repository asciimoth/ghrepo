package inspect

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
)

const testControl = "Package: hello\nVersion: 1.2.0-1\nArchitecture: amd64\nMaintainer: Fixture <fixture@example.invalid>\nDescription: test package\n"

type tarEntry struct {
	header tar.Header
	body   string
}

func regularEntry(name, body string) tarEntry {
	return tarEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(body))}, body: body}
}

func testTar(t testing.TB, format tar.Format, entries []tarEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, entry := range entries {
		hdr := entry.header
		hdr.Format = format
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func testGzip(t testing.TB, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

// Keep ar construction independent of the production envelope reader.
func testDEB(t testing.TB, control, data []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	archive.WriteString("!<arch>\n")
	for _, entry := range []struct {
		name string
		body []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", control}, {"data.tar.gz", data}} {
		fmt.Fprintf(&archive, "%-16s%-12d%-6d%-6d%-8o%-10d`\n", entry.name+"/", 0, 0, 0, 0644, len(entry.body))
		archive.Write(entry.body)
		if len(entry.body)%2 != 0 {
			archive.WriteByte('\n')
		}
	}
	return archive.Bytes()
}

func testDEBEntries(t testing.TB, entries []tarEntry) []byte {
	t.Helper()
	return testDEB(t, testGzip(t, testTar(t, tar.FormatUSTAR, entries)), testGzip(t, testTar(t, tar.FormatUSTAR, nil)))
}

func requireDEBMetadata(t testing.TB, raw []byte) Metadata {
	t.Helper()
	m, err := Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := contract.Identity{Name: "hello", Version: "1.2.0-1", Architecture: "amd64"}
	if m.Format != "deb" || m.Identity != want || m.Fields["Maintainer"] != "Fixture <fixture@example.invalid>" {
		t.Fatalf("unexpected metadata: %+v", m)
	}
	return m
}

// Exercise archive variations independently so root entries, order, and producer
// tar formats cannot hide another rejection like the md5sums regression.
func TestDEBControlArchiveCompatibility(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatUSTAR, tar.FormatGNU, tar.FormatPAX} {
		for _, root := range []string{"", ".", "./"} {
			for _, prefix := range []string{"", "./"} {
				for _, position := range []int{0, 5, 10} {
					name := fmt.Sprintf("%s/root=%q/prefix=%q/control=%d", format, root, prefix, position)
					t.Run(name, func(t *testing.T) {
						entries := standardControlEntries(prefix)
						entries = append(entries[:position], append([]tarEntry{regularEntry(prefix+"control", testControl)}, entries[position:]...)...)
						if root != "" {
							entries = append([]tarEntry{{header: tar.Header{Name: root, Typeflag: tar.TypeDir, Mode: 0755}}}, entries...)
						}
						data := testDEB(t, testGzip(t, testTar(t, format, entries)), testGzip(t, testTar(t, format, nil)))
						requireDEBMetadata(t, data)
					})
				}
			}
		}
	}
}

func TestDEBArchitectureCompatibility(t *testing.T) {
	for _, arch := range []string{"all", "amd64", "arm64", "armhf", "i386", "riscv64gc"} {
		t.Run(arch, func(t *testing.T) {
			control := strings.Replace(testControl, "Architecture: amd64", "Architecture: "+arch, 1)
			data := testDEBEntries(t, []tarEntry{regularEntry("./control", control)})
			m, err := Bytes(data)
			if err != nil {
				t.Fatal(err)
			}
			want := contract.Identity{Name: "hello", Version: "1.2.0-1", Architecture: arch}
			if m.Format != "deb" || m.Identity != want || m.Fields["Architecture"] != arch {
				t.Fatalf("unexpected metadata: %+v", m)
			}
		})
	}
}

func standardControlEntries(prefix string) []tarEntry {
	var entries []tarEntry
	for _, name := range []string{"md5sums", "conffiles", "triggers", "shlibs", "symbols", "preinst", "postinst", "prerm", "postrm", "config"} {
		entry := regularEntry(prefix+name, "")
		if strings.HasSuffix(name, "inst") || strings.HasSuffix(name, "rm") || name == "config" {
			entry = regularEntry(prefix+name, "#!/bin/sh\nexit 0\n")
			entry.header.Mode = 0755
		}
		entries = append(entries, entry)
	}
	entries[0].body = "b1946ac92492d2347c6235b4d2611184  usr/share/hello/message\n"
	entries[0].header.Size = int64(len(entries[0].body))
	return entries
}

func TestDEBControlArchiveRejectsInvalidEntries(t *testing.T) {
	valid := regularEntry("control", testControl)
	for _, name := range []string{"../md5sums", "./../md5sums", "dir/../../md5sums", "dir/../md5sums", "/md5sums", ""} {
		t.Run("path="+name, func(t *testing.T) {
			if _, err := Bytes(testDEBEntries(t, []tarEntry{valid, regularEntry(name, "bad")})); err == nil {
				t.Fatal("accepted unsafe control member")
			}
		})
	}
	for _, name := range []string{"control", "./control", "md5sums"} {
		for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock, 'Z'} {
			t.Run(fmt.Sprintf("%s/type=%c", name, kind), func(t *testing.T) {
				entry := tarEntry{header: tar.Header{Name: name, Typeflag: kind}}
				if kind == tar.TypeSymlink || kind == tar.TypeLink {
					entry.header.Linkname = "control"
				}
				if _, err := Bytes(testDEBEntries(t, []tarEntry{valid, entry})); err == nil {
					t.Fatal("accepted unsupported control entry type")
				}
			})
		}
	}
	for _, tc := range []struct {
		name    string
		entries []tarEntry
	}{
		{"missing", standardControlEntries("./")},
		{"empty archive", nil},
		{"duplicate same name", []tarEntry{valid, valid}},
		{"duplicate alternate name", []tarEntry{valid, regularEntry("./control", testControl)}},
		{"control directory", []tarEntry{{header: tar.Header{Name: "control", Typeflag: tar.TypeDir}}}},
		{"empty control", []tarEntry{regularEntry("control", "")}},
		{"oversize control", []tarEntry{regularEntry("control", testControl+strings.Repeat("x", 1024*1024))}},
		{"root regular file", []tarEntry{valid, regularEntry(".", "")}},
		{"nested control only", []tarEntry{regularEntry("sub/control", testControl)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Bytes(testDEBEntries(t, tc.entries)); err == nil {
				t.Fatal("accepted invalid control archive")
			}
		})
	}
}

type controlFieldCase struct {
	name, control, key, value string
}

func controlFieldCases() []controlFieldCase {
	return []controlFieldCase{
		{"plain", testControl, "Description", "test package"},
		{"CRLF", strings.ReplaceAll(testControl, "\n", "\r\n"), "Description", "test package"},
		{"trailing stanza separator", testControl + "\n\n", "Description", "test package"},
		{"leading stanza separator", "\n" + testControl, "Description", "test package"},
		{"space continuation", testControl + " long description\n .\n final paragraph\n", "Description", "test package\n long description\n .\n final paragraph"},
		{"tab continuation", testControl + "\tlong description\n", "Description", "test package\n\tlong description"},
		{"folded depends", testControl + "Depends: libc6 (>= 2.36),\n\tbase-files | alternative\n", "Depends", "libc6 (>= 2.36),\n\tbase-files | alternative"},
		{"empty first line", testControl + "X-Notes:\n first line\n\tsecond line\n", "X-Notes", "\n first line\n\tsecond line"},
		{"UTF-8", testControl + " café — 日本語\n", "Description", "test package\n café — 日本語"},
		{"mixed case", strings.ReplaceAll(testControl, "Package:", "pAcKaGe:"), "Package", "hello"},
		{"extension field", testControl + "X.Vendor_1+: value\n", "X.Vendor_1+", "value"},
		{"horizontal whitespace", strings.Replace(testControl, "Package: hello", "Package:\thello\t ", 1), "Package", "hello"},
	}
}

func TestDEBControlFieldsCompatibility(t *testing.T) {
	for _, tc := range controlFieldCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := requireDEBMetadata(t, testDEBEntries(t, []tarEntry{regularEntry("control", tc.control)}))
			if m.Fields[tc.key] != tc.value {
				t.Fatalf("%s = %q, want %q", tc.key, m.Fields[tc.key], tc.value)
			}
			if tc.key == "Depends" && (len(m.Requires) != 2 || m.Requires[0] != strings.TrimSpace(strings.Split(tc.value, ",")[0]) || m.Requires[1] != strings.TrimSpace(strings.Split(tc.value, ",")[1])) {
				t.Fatalf("dependencies = %#v", m.Requires)
			}
		})
	}
}

func TestDEBEmptySectionCompatibility(t *testing.T) {
	control := testControl + "Section: \nArchitecture-Variant: amd64v1\n"
	meta := requireDEBMetadata(t, testDEBEntries(t, []tarEntry{regularEntry("control", control)}))
	if _, ok := meta.Fields["Section"]; ok {
		t.Fatal("empty optional section was copied to package metadata")
	}
}

func TestDEBControlFieldsRejectInvalid(t *testing.T) {
	cases := map[string]string{
		"duplicate":      testControl + "Package: other\n",
		"case duplicate": testControl + "pACKAGE: other\n",
		"second stanza":  testControl + "\nX-Extra: value\n",
		"second package": testControl + "\n" + testControl,
		"orphan space":   " continuation\n" + testControl,
		"orphan tab":     "\tcontinuation\n" + testControl,
		"empty field":    testControl + "X-Empty: \t\n",
		"missing colon":  testControl + "X-Extra value\n",
		"empty name":     testControl + ": value\n",
		"space in name":  testControl + "X Extra: value\n",
		"UTF-8 name":     testControl + "X-é: value\n",
		"hyphen prefix":  testControl + "-Extra: value\n",
		"comment":        testControl + "# comment\n",
		"comment prefix": testControl + "#Extra: value\n",
		"invalid UTF-8":  testControl + "X-Extra: \xff\n",
		"NUL":            testControl + "X-Extra: before\x00after\n",
		"embedded CR":    testControl + "X-Extra: before\rafter\n",
	}
	for _, field := range []string{"Package", "Version", "Architecture", "Maintainer", "Description"} {
		for _, line := range strings.Split(testControl, "\n") {
			if strings.HasPrefix(line, field+":") {
				cases["missing "+field] = strings.Replace(testControl, line+"\n", "", 1)
				cases["empty "+field] = strings.Replace(testControl, line, field+":", 1)
			}
		}
	}
	for name, control := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Bytes(testDEBEntries(t, []tarEntry{regularEntry("control", control)})); err == nil {
				t.Fatal("accepted invalid control fields")
			}
		})
	}
}

func TestDEBControlSizeLimit(t *testing.T) {
	control := testControl + "X-Long: "
	control += strings.Repeat("x", 1024*1024-len(control))
	m := requireDEBMetadata(t, testDEBEntries(t, []tarEntry{regularEntry("control", control)}))
	if len(m.Fields["X-Long"]) != 1024*1024-len(testControl)-len("X-Long: ") {
		t.Fatal("long field was truncated")
	}
}

func TestDEBArchiveCorruption(t *testing.T) {
	control := testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("control", testControl), regularEntry("md5sums", "checksum\n")})
	payload := testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("usr/share/hello/message", "hello\n")})
	for _, member := range []string{"control", "data"} {
		raw := control
		if member == "data" {
			raw = payload
		}
		for _, tc := range []struct {
			name   string
			change func([]byte) []byte
		}{
			{"bad tar checksum", func(b []byte) []byte { b[0] ^= 1; return testGzip(t, b) }},
			{"truncated tar header", func(b []byte) []byte { return testGzip(t, b[:100]) }},
			{"truncated tar body", func(b []byte) []byte { return testGzip(t, b[:513]) }},
			{"bad gzip checksum", func(b []byte) []byte { b = testGzip(t, b); b[len(b)-8] ^= 1; return b }},
			{"truncated gzip trailer", func(b []byte) []byte { b = testGzip(t, b); return b[:len(b)-4] }},
			{"bad gzip header", func(b []byte) []byte { b = testGzip(t, b); b[0] = 0; return b }},
		} {
			t.Run(member+"/"+tc.name, func(t *testing.T) {
				ctrl, data := testGzip(t, control), testGzip(t, payload)
				bad := tc.change(bytes.Clone(raw))
				if member == "control" {
					ctrl = bad
				} else {
					data = bad
				}
				if _, err := Bytes(testDEB(t, ctrl, data)); err == nil {
					t.Fatal("accepted corrupt archive")
				}
			})
		}
	}
	// A valid control file must not hide a corrupt later member.
	t.Run("truncated auxiliary file", func(t *testing.T) {
		if _, err := Bytes(testDEB(t, testGzip(t, control[:1537]), testGzip(t, payload))); err == nil {
			t.Fatal("accepted truncated md5sums")
		}
	})
}

func TestDEBPayloadCompatibility(t *testing.T) {
	control := testGzip(t, testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("control", testControl)}))
	entries := []tarEntry{
		{header: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}},
		regularEntry("./usr/share/hello/message", "hello\n"),
		{header: tar.Header{Name: "./usr/share/hello/relative", Typeflag: tar.TypeSymlink, Linkname: "message"}},
		{header: tar.Header{Name: "./usr/share/hello/absolute", Typeflag: tar.TypeSymlink, Linkname: "/usr/share/hello/message"}},
		{header: tar.Header{Name: "./usr/share/hello/hard", Typeflag: tar.TypeLink, Linkname: "./usr/share/hello/message"}},
	}
	for _, format := range []tar.Format{tar.FormatUSTAR, tar.FormatGNU, tar.FormatPAX} {
		t.Run(format.String(), func(t *testing.T) {
			requireDEBMetadata(t, testDEB(t, control, testGzip(t, testTar(t, format, entries))))
		})
	}
	for _, name := range []string{"/etc/unsafe", "../unsafe", "./../../unsafe"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Bytes(testDEB(t, control, testGzip(t, testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry(name, "bad")})))); err == nil {
				t.Fatal("accepted unsafe payload path")
			}
		})
	}
}

func TestDEBExtendedTarHeaders(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatGNU, tar.FormatPAX} {
		t.Run(format.String(), func(t *testing.T) {
			entry := regularEntry("metadata/"+strings.Repeat("long-name-", 20), "extra metadata\n")
			if format == tar.FormatPAX {
				entry.header.PAXRecords = map[string]string{"VENDOR.comment": "extra metadata"}
			}
			control := testGzip(t, testTar(t, format, []tarEntry{entry, regularEntry("./control", testControl)}))
			requireDEBMetadata(t, testDEB(t, control, testGzip(t, testTar(t, format, nil))))
		})
	}
	// The Go writer normalizes TypeRegA to TypeReg. Construct an old-style
	// regular file header explicitly so this case tests bytes seen in the wild.
	for _, v7 := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy regular/v7=%v", v7), func(t *testing.T) {
			control := testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("control", testControl)})
			control[156] = 0 // Legacy regular file type; tar.Reader normalizes it.
			if v7 {
				clear(control[257:512])
			}
			for i := 148; i < 156; i++ {
				control[i] = ' '
			}
			sum := 0
			for _, c := range control[:512] {
				sum += int(c)
			}
			copy(control[148:156], fmt.Sprintf("%06o\x00 ", sum))
			requireDEBMetadata(t, testDEB(t, testGzip(t, control), testGzip(t, testTar(t, tar.FormatUSTAR, nil))))
		})
	}
}

func TestDEBEnvelopeCompatibilityAndFailures(t *testing.T) {
	valid := testDEBEntries(t, []tarEntry{regularEntry("control", testControl)})
	t.Run("ar names without slash", func(t *testing.T) {
		raw := bytes.Clone(valid)
		for _, name := range []string{"debian-binary", "control.tar.gz", "data.tar.gz"} {
			raw = bytes.Replace(raw, []byte(name+"/"), []byte(name+" "), 1)
		}
		requireDEBMetadata(t, raw)
	})
	t.Run("all truncated prefixes", func(t *testing.T) {
		for n := 0; n < len(valid); n++ {
			if _, err := Bytes(valid[:n]); err == nil {
				t.Fatalf("accepted truncated package of %d/%d bytes", n, len(valid))
			}
		}
	})
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"bad ar magic", func(b []byte) []byte { b[0] = 0; return b }},
		{"bad ar header trailer", func(b []byte) []byte { b[66] = 'x'; return b }},
		{"negative member size", func(b []byte) []byte { copy(b[56:66], "-1        "); return b }},
		{"oversize member", func(b []byte) []byte { copy(b[56:66], "9999999999"); return b }},
		{"nondecimal member size", func(b []byte) []byte { copy(b[56:66], "x         "); return b }},
		{"unsupported version", func(b []byte) []byte { return bytes.Replace(b, []byte("2.0\n"), []byte("3.0\n"), 1) }},
		{"truncated extra member", func(b []byte) []byte { return append(b, b[8:71]...) }},
		{"out of order", func(b []byte) []byte { return bytes.Replace(b, []byte("control.tar.gz"), []byte("data.tar.gz  "), 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Bytes(tc.change(bytes.Clone(valid))); err == nil {
				t.Fatal("accepted invalid DEB envelope")
			}
		})
	}
	for _, member := range []string{"control", "data"} {
		for _, suffix := range []string{"tar.xz", "tar.zst", "tar"} {
			t.Run("unsupported/"+member+"/"+suffix, func(t *testing.T) {
				old := fmt.Sprintf("%-16s", member+".tar.gz/")
				name := fmt.Sprintf("%-16s", member+"."+suffix+"/")
				raw := bytes.Replace(valid, []byte(old), []byte(name), 1)
				if _, err := Bytes(raw); !errors.Is(err, contract.ErrUnsupportedDEBCompression) {
					t.Fatalf("unsupported compression error = %v", err)
				}
			})
		}
	}
}

func FuzzDEBInspection(f *testing.F) {
	for _, name := range []string{"hello_1.2.0-1_amd64.deb", "hello_compat.deb", "hello_unsafe.deb", "hello_malformed.deb", "hello_signed-envelope.deb"} {
		raw, err := os.ReadFile(filepath.Join("../contract/testdata/packages", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1024*1024 {
			t.Skip()
		}
		// Keep this target on the DEB parser, including malformed envelopes.
		_, _ = deb(raw)
	})
}

func appendARMember(raw []byte, name string, body []byte) []byte {
	var out bytes.Buffer
	out.Write(raw)
	fmt.Fprintf(&out, "%-16s%-12d%-6d%-6d%-8o%-10d`\n", name+"/", 0, 0, 0, 0644, len(body))
	out.Write(body)
	if len(body)%2 != 0 {
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func debWithExtensions(t testing.TB, name string, body []byte, position uint8) []byte {
	t.Helper()
	base := testDEBEntries(t, []tarEntry{regularEntry("control", testControl)})
	// Positions after debian-binary, after control, and after data.
	offset := 72
	if position%3 > 0 {
		var size int
		if _, err := fmt.Sscanf(string(base[offset+48:offset+58]), "%d", &size); err != nil {
			t.Fatal(err)
		}
		offset += 60 + size + size%2
	}
	if position%3 == 2 {
		offset = len(base)
	}
	return append(appendARMember(base[:offset], name, body), base[offset:]...)
}

func TestDEBExtensionMembers(t *testing.T) {
	for _, body := range [][]byte{nil, []byte("odd"), []byte("even")} {
		for pos := uint8(0); pos < 3; pos++ {
			t.Run(fmt.Sprintf("position=%d/size=%d", pos, len(body)), func(t *testing.T) {
				requireDEBMetadata(t, debWithExtensions(t, "_gpgorigin", body, pos))
				raw := debWithExtensions(t, "extension", body, pos)
				_, err := Bytes(raw)
				if (err == nil) != (pos == 2) {
					t.Fatalf("unmarked extension at position %d: %v", pos, err)
				}
			})
		}
	}
	raw := debWithExtensions(t, "_gpgorigin", []byte("odd"), 2)
	for n := len(raw) - 4; n < len(raw); n++ {
		if _, err := Bytes(raw[:n]); err == nil {
			t.Fatalf("accepted truncated extension at %d", n)
		}
	}
}

func FuzzDEBExtensionMember(f *testing.F) {
	for pos := uint8(0); pos < 3; pos++ {
		f.Add("_gpgorigin", []byte("signature"), pos)
		f.Add("extension", []byte{}, pos)
	}
	f.Fuzz(func(t *testing.T, name string, body []byte, pos uint8) {
		if len(name) > 15 || len(body) > 64*1024 {
			t.Skip()
		}
		raw := debWithExtensions(t, name, body, pos)
		_, _ = Bytes(raw)
	})
}
