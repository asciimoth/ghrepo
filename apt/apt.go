// Package apt builds and independently verifies the version 1 APT index.
package apt

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
)

// Build writes a complete signed deb directory to a new output directory.
// Package bytes are read from the verified local cache.
func Build(out, cacheRoot string, r contract.Repository, m contract.Manifest, local contract.Local) error {
	return buildAt(out, cacheRoot, r, m, local, time.Now().UTC())
}

func buildAt(out, cacheRoot string, r contract.Repository, m contract.Manifest, local contract.Local, when time.Time) (buildErr error) {
	if err := m.Validate(r); err != nil {
		return err
	}
	if local.GPG.KeyID == "" {
		return errors.New("APT signing key is not configured")
	}
	if err := os.Mkdir(out, 0755); err != nil {
		return err
	}
	defer func() {
		if buildErr != nil {
			_ = os.RemoveAll(out)
		}
	}()
	entries := active(m)
	var plain bytes.Buffer
	for _, p := range entries {
		name := filepath.Join(cacheRoot, "sha256", p.SHA256)
		if err := cache.VerifyFile(name, p); err != nil {
			return fmt.Errorf("package %s: %w", p.Path, err)
		}
		meta, size, digest, err := inspect.File(name, r.Downloads.MaxBytes)
		if err != nil {
			return err
		}
		if meta.Format != "deb" || meta.Identity != p.Identity || size != p.Size || digest != p.SHA256 {
			return fmt.Errorf("package %s: inspected identity differs", p.Path)
		}
		if err := writeControl(&plain, meta.Fields); err != nil {
			return err
		}
		fmt.Fprintf(&plain, "Filename: %s\nSize: %d\nSHA256: %s\n\n", strings.TrimPrefix(p.Path, "deb/"), p.Size, p.SHA256)
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.ModTime = time.Unix(0, 0)
	zw.OS = 255
	if _, err := zw.Write(plain.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	index := zipped.Bytes()
	hash := sha256.Sum256(index)
	plainHash := sha256.Sum256(plain.Bytes())
	release := fmt.Sprintf("Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\nComponents: %s\nArchitectures: %s\nAcquire-By-Hash: no\nSHA256:\n %x %d Packages\n %x %d Packages.gz\n", r.RPM.ID, r.RPM.Name, r.APT.Suite, r.APT.Suite, when.UTC().Format(time.RFC1123), r.APT.Component, strings.Join(r.APT.Architectures, " "), plainHash, plain.Len(), hash, len(index))
	if err := os.WriteFile(filepath.Join(out, "Packages"), plain.Bytes(), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "Packages.gz"), index, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "Release"), []byte(release), 0644); err != nil {
		return err
	}
	cmd := gpg(local, "--batch", "--yes", "--local-user", local.GPG.KeyID, "--digest-algo", "SHA256", "--clearsign", "--output", filepath.Join(out, "InRelease"), filepath.Join(out, "Release"))
	if err := cmd.Run(); err != nil {
		return errors.New("APT Release signing failed")
	}
	return nil
}

func writeControl(w io.Writer, fields map[string]string) error {
	preferred := []string{"Package", "Version", "Architecture", "Maintainer", "Installed-Size", "Depends", "Pre-Depends", "Provides", "Conflicts", "Breaks", "Replaces", "Recommends", "Suggests", "Enhances", "Section", "Priority", "Homepage", "Description"}
	used := map[string]bool{}
	seen := map[string]bool{}
	for key := range fields {
		lower := strings.ToLower(key)
		if seen[lower] {
			return fmt.Errorf("duplicate DEB control field %s", key)
		}
		seen[lower] = true
		switch lower {
		case "filename", "size", "sha256":
			return fmt.Errorf("DEB control field %s conflicts with generated index", key)
		}
	}
	for _, standard := range preferred {
		for key, value := range fields {
			if strings.EqualFold(key, standard) {
				if err := controlField(w, key, value); err != nil {
					return err
				}
				used[key] = true
			}
		}
	}
	var extra []string
	for key := range fields {
		if !used[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		if err := controlField(w, key, fields[key]); err != nil {
			return err
		}
	}
	return nil
}

func controlField(w io.Writer, key, value string) error {
	if strings.ContainsAny(value, "\r\x00") {
		return fmt.Errorf("invalid DEB field %s", key)
	}
	for n, line := range strings.Split(value, "\n") {
		if n > 0 && !strings.HasPrefix(line, " ") {
			return fmt.Errorf("invalid DEB field continuation %s", key)
		}
	}
	_, err := fmt.Fprintf(w, "%s: %s\n", key, value)
	return err
}

func gpg(local contract.Local, args ...string) *exec.Cmd {
	if local.GPG.Keyring != "" {
		args = append([]string{"--no-default-keyring", "--keyring", local.GPG.Keyring}, args...)
	}
	cmd := exec.Command("gpg", args...)
	if local.GPG.Home != "" {
		cmd.Env = append(os.Environ(), "GNUPGHOME="+local.GPG.Home)
	}
	return cmd
}

func active(m contract.Manifest) []contract.Package {
	var entries []contract.Package
	for _, p := range m.Packages {
		if p.Format == "deb" && p.State == "active" {
			entries = append(entries, p)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// Verify reads signed metadata without using the generation parser. The public
// key file must be an exported OpenPGP keyring, such as KEY.gpg.
func Verify(dir, publicKey string, r contract.Repository, m contract.Manifest) error {
	if err := m.Validate(r); err != nil {
		return err
	}
	if _, err := os.Stat(publicKey); err != nil {
		return err
	}
	verified, err := os.CreateTemp("", "ghrepo-verified-release-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(verified.Name()) }()
	if err := verified.Close(); err != nil {
		return err
	}
	cmd := exec.Command("gpgv", "--status-fd", "1", "--keyring", publicKey, "--output", verified.Name(), filepath.Join(dir, "InRelease"))
	status, err := cmd.Output()
	if err != nil {
		return errors.New("APT Release signature verification failed")
	}
	validAlgorithm := false
	for _, line := range strings.Split(string(status), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 10 && parts[0] == "[GNUPG:]" && parts[1] == "VALIDSIG" && (parts[8] == "1" || parts[8] == "2" || parts[8] == "3") && parts[9] == "8" {
			validAlgorithm = true
		}
	}
	if !validAlgorithm {
		return errors.New("unsupported APT Release signature algorithm")
	}
	cleared, err := os.ReadFile(verified.Name())
	if err != nil {
		return err
	}
	release, err := os.ReadFile(filepath.Join(dir, "Release"))
	if err != nil {
		return err
	}
	if !bytes.Equal(cleared, release) {
		return errors.New("APT Release differs from signed content")
	}
	wants, err := readRelease(release, r)
	if err != nil {
		return err
	}
	index, err := os.ReadFile(filepath.Join(dir, "Packages.gz"))
	if err != nil {
		return err
	}
	h := sha256.Sum256(index)
	if len(index) != wants["Packages.gz"].size || hex.EncodeToString(h[:]) != wants["Packages.gz"].hash {
		return errors.New("APT Packages.gz hash or size mismatch")
	}
	zr, err := gzip.NewReader(bytes.NewReader(index))
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(zr, 128<<20))
	if err != nil {
		return err
	}
	if err := zr.Close(); err != nil {
		return err
	}
	if len(data) >= 128<<20 {
		return errors.New("APT Packages index exceeds limit")
	}
	plainFile, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		return err
	}
	h = sha256.Sum256(plainFile)
	if len(plainFile) != wants["Packages"].size || hex.EncodeToString(h[:]) != wants["Packages"].hash || !bytes.Equal(plainFile, data) {
		return errors.New("APT Packages hash, size, or content mismatch")
	}
	return readPackages(data, active(m))
}

type checksum struct {
	hash string
	size int
}

func readRelease(data []byte, r contract.Repository) (map[string]checksum, error) {
	fields := map[string]string{}
	checksums := map[string]checksum{}
	inHash := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, " ") {
			if !inHash {
				return nil, errors.New("invalid APT Release hash entry")
			}
			parts := strings.Fields(line)
			if len(parts) != 3 || (parts[2] != "Packages" && parts[2] != "Packages.gz") || checksums[parts[2]].hash != "" || len(parts[0]) != 64 {
				return nil, errors.New("invalid APT Release index path")
			}
			if _, err := hex.DecodeString(parts[0]); err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(parts[1])
			if err != nil || n < 0 {
				return nil, errors.New("invalid APT Release index size")
			}
			checksums[parts[2]] = checksum{parts[0], n}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if _, exists := fields[key]; !ok || exists {
			return nil, errors.New("invalid APT Release field")
		}
		fields[key] = strings.TrimSpace(value)
		inHash = key == "SHA256"
	}
	if _, err := time.Parse(time.RFC1123, fields["Date"]); err != nil {
		return nil, errors.New("invalid APT Release date")
	}
	if fields["Suite"] != r.APT.Suite || fields["Codename"] != r.APT.Suite || fields["Components"] != r.APT.Component || fields["Architectures"] != strings.Join(r.APT.Architectures, " ") || fields["Acquire-By-Hash"] != "no" || checksums["Packages"].hash == "" || checksums["Packages.gz"].hash == "" {
		return nil, errors.New("APT Release configuration mismatch")
	}
	return checksums, nil
}

func readPackages(data []byte, expected []contract.Package) error {
	want := make(map[string]contract.Package, len(expected))
	for _, p := range expected {
		want[p.Path] = p
	}
	seen := map[string]bool{}
	if len(data) == 0 && len(expected) == 0 {
		return nil
	}
	if !bytes.HasSuffix(data, []byte("\n\n")) {
		return errors.New("invalid APT Packages termination")
	}
	for _, stanza := range strings.Split(strings.TrimSuffix(string(data), "\n\n"), "\n\n") {
		fields := map[string]string{}
		last := ""
		for _, line := range strings.Split(stanza, "\n") {
			if strings.HasPrefix(line, " ") {
				if last == "" {
					return errors.New("invalid APT Packages continuation")
				}
				fields[last] += "\n" + line
				continue
			}
			key, value, ok := strings.Cut(line, ": ")
			if _, exists := fields[key]; !ok || exists {
				return errors.New("invalid APT Packages field")
			}
			fields[key], last = value, key
		}
		path := fields["Filename"]
		if err := contract.SafePath(path); err != nil {
			return err
		}
		canonical := "deb/" + path
		p, ok := want[canonical]
		if !ok || seen[canonical] {
			return fmt.Errorf("unexpected or duplicate APT package %s", path)
		}
		seen[canonical] = true
		if fields["Package"] != p.Identity.Name || fields["Version"] != p.Identity.Version || fields["Architecture"] != p.Identity.Architecture || fields["Size"] != strconv.FormatInt(p.Size, 10) || fields["SHA256"] != p.SHA256 {
			return fmt.Errorf("APT package metadata mismatch: %s", path)
		}
	}
	if len(seen) != len(want) {
		return errors.New("APT index omits an active package")
	}
	return nil
}
