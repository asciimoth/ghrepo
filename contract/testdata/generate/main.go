// Command generate creates deterministic DEB fixtures. Run with go run . from
// this directory. Normal tests read the checked-in files and do not run this.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	base := filepath.Join("..", "packages")
	control := "Package: hello\nVersion: 1.2.0-1\nArchitecture: amd64\nMaintainer: Fixture <fixture@example.invalid>\nDescription: small offline fixture\n"
	write(base, "hello_1.2.0-1_amd64.deb", deb(control, "control"))
	write(base, "hello_2.0.0-1_amd64.deb", deb("Package: hello\nVersion: 2.0.0-1\nArchitecture: amd64\nMaintainer: Fixture <fixture@example.invalid>\nDescription: small offline fixture\n", "control"))
	write(base, "hello_changed.deb", deb(control+"X-Fixture: changed bytes\n", "control"))
	write(base, "hello_unsafe.deb", deb(control, "../control"))
	write(base, "hello_malformed.deb", []byte("!<arch>\ninvalid archive"))
}

func write(base, name string, b []byte) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(base, name), b, 0o644); err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	fmt.Printf("%s %d %s\n", name, len(b), hex.EncodeToString(h[:]))
}

func deb(control, controlPath string) []byte {
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	ar(&out, "debian-binary", []byte("2.0\n"))
	ar(&out, "control.tar.gz", tarball(controlPath, []byte(control)))
	ar(&out, "data.tar.gz", tarball("./usr/share/doc/hello/README", []byte("hello\n")))
	return out.Bytes()
}

func ar(out *bytes.Buffer, name string, body []byte) {
	header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n", name+"/", 0, 0, 0, 0o644, len(body))
	if len(header) != 60 {
		panic("bad ar header")
	}
	out.WriteString(header)
	out.Write(body)
	if len(body)%2 != 0 {
		out.WriteByte('\n')
	}
}

func tarball(name string, body []byte) []byte {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		panic(err)
	}
	gz.ModTime = time.Unix(0, 0)
	gz.OS = 255
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
		panic(err)
	}
	if _, err := tw.Write(body); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
