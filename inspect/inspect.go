// Package inspect reads package metadata from verified archive bytes.
package inspect

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/asciimoth/ghrepo/contract"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

type Requirement struct {
	Name    string
	Version string
	Flags   uint32
}

type Metadata struct {
	Identity      contract.Identity
	Format        string
	Fields        map[string]string
	Requires      []string
	Requirements  []Requirement
	SignatureTags []string
}

// File inspects a regular package file and hashes its exact bytes.
func File(name string, max int64) (Metadata, int64, string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return Metadata{}, 0, "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > max {
		return Metadata{}, 0, "", errors.New("invalid package file size or type")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return Metadata{}, 0, "", err
	}
	m, err := Bytes(data)
	if err != nil {
		return Metadata{}, 0, "", err
	}
	h := sha256.Sum256(data)
	return m, int64(len(data)), hex.EncodeToString(h[:]), nil
}

func Bytes(data []byte) (Metadata, error) {
	if bytes.HasPrefix(data, []byte("!<arch>\n")) {
		return deb(data)
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0xed, 0xab, 0xee, 0xdb}) {
		return rpm(data)
	}
	return Metadata{}, errors.New("unsupported package format")
}

func deb(data []byte) (Metadata, error) {
	if err := contract.CheckDEBFormat(data); err != nil {
		return Metadata{}, err
	}
	data = data[8:]
	var control []byte
	for member := 0; member < 3; member++ {
		n := 0
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data[48:58])), "%d", &n); err != nil {
			return Metadata{}, contract.ErrMalformedDEB
		}
		body := data[60 : 60+n]
		if member == 2 {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				return Metadata{}, fmt.Errorf("invalid DEB data gzip: %w", err)
			}
			tr := tar.NewReader(zr)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return Metadata{}, fmt.Errorf("invalid DEB data tar: %w", err)
				}
				clean := path.Clean(hdr.Name)
				if strings.HasPrefix(hdr.Name, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
					return Metadata{}, errors.New("unsafe DEB data path")
				}
				if _, err := io.Copy(io.Discard, tr); err != nil {
					return Metadata{}, fmt.Errorf("invalid DEB data member: %w", err)
				}
			}
			if _, err := io.Copy(io.Discard, zr); err != nil {
				return Metadata{}, fmt.Errorf("invalid DEB data gzip: %w", err)
			}
			if err := zr.Close(); err != nil {
				return Metadata{}, err
			}
		}
		if member == 1 {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				return Metadata{}, fmt.Errorf("invalid DEB control gzip: %w", err)
			}
			tr := tar.NewReader(zr)
			found := false
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return Metadata{}, fmt.Errorf("invalid DEB control tar: %w", err)
				}
				clean := path.Clean(hdr.Name)
				if (hdr.Name != "control" && hdr.Name != "./control") || clean != "control" || hdr.Typeflag != tar.TypeReg || found || hdr.Size > 1024*1024 || hdr.Size < 1 {
					return Metadata{}, errors.New("unsafe or unsupported DEB control path")
				}
				control, err = io.ReadAll(io.LimitReader(tr, 1024*1024+1))
				if err != nil {
					return Metadata{}, err
				}
				found = true
			}
			if !found {
				return Metadata{}, errors.New("missing DEB control file")
			}
			if _, err := io.Copy(io.Discard, zr); err != nil {
				return Metadata{}, fmt.Errorf("invalid DEB control gzip: %w", err)
			}
			if err := zr.Close(); err != nil {
				return Metadata{}, err
			}
		}
		data = data[60+n:]
		if n%2 != 0 {
			data = data[1:]
		}
	}
	if !utf8.Valid(control) {
		return Metadata{}, errors.New("invalid DEB control UTF-8")
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	canonical := map[string]string{"package": "Package", "version": "Version", "architecture": "Architecture", "maintainer": "Maintainer", "description": "Description", "depends": "Depends"}
	scanner := bufio.NewScanner(bytes.NewReader(control))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	previous := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") {
			if previous == "" {
				return Metadata{}, errors.New("invalid DEB control continuation")
			}
			fields[previous] += "\n" + line
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		lower := strings.ToLower(key)
		if !ok || key == "" || strings.TrimSpace(value) == "" || seen[lower] {
			return Metadata{}, errors.New("invalid or duplicate DEB control field")
		}
		for _, c := range key {
			if c < 'A' || c > 'Z' {
				if c < 'a' || c > 'z' {
					if c != '-' && (c < '0' || c > '9') {
						return Metadata{}, errors.New("invalid DEB control field")
					}
				}
			}
		}
		seen[lower] = true
		if standard, ok := canonical[lower]; ok {
			key = standard
		}
		fields[key] = strings.TrimSpace(value)
		previous = key
	}
	if scanner.Err() != nil {
		return Metadata{}, scanner.Err()
	}
	for _, key := range []string{"Package", "Version", "Architecture", "Maintainer", "Description"} {
		if fields[key] == "" {
			return Metadata{}, fmt.Errorf("missing DEB control field %s", key)
		}
	}
	return Metadata{Identity: contract.Identity{Name: fields["Package"], Version: fields["Version"], Architecture: fields["Architecture"]}, Format: "deb", Fields: fields, Requires: splitDependencies(fields["Depends"])}, nil
}
func splitDependencies(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func rpm(data []byte) (Metadata, error) {
	if err := contract.CheckRPMFormat(data); err != nil {
		return Metadata{}, err
	}
	hdr, err := rpmutils.ReadHeader(bytes.NewReader(data))
	if err != nil {
		return Metadata{}, fmt.Errorf("invalid RPM header: %w", err)
	}
	n, err := hdr.GetNEVRA()
	if err != nil {
		return Metadata{}, fmt.Errorf("invalid RPM identity: %w", err)
	}
	epoch := n.Epoch
	if epoch == "" {
		epoch = "0"
	}
	fields := map[string]string{}
	for key, tag := range map[string]int{"Summary": rpmutils.SUMMARY, "Description": rpmutils.DESCRIPTION, "License": rpmutils.LICENSE, "URL": rpmutils.URL} {
		if v, e := hdr.GetString(tag); e == nil {
			fields[key] = v
		}
	}
	requires, _ := hdr.GetStrings(rpmutils.REQUIRENAME)
	versions, _ := hdr.GetStrings(rpmutils.REQUIREVERSION)
	flags, _ := hdr.GetUint32s(rpmutils.REQUIREFLAGS)
	if len(requires) > 0 && (len(versions) != len(requires) || len(flags) != len(requires)) {
		return Metadata{}, errors.New("invalid RPM dependency fields")
	}
	dependencies := make([]Requirement, len(requires))
	for i, name := range requires {
		dependencies[i] = Requirement{Name: name, Version: versions[i], Flags: flags[i]}
	}
	var signatures []string
	for _, item := range []struct {
		name string
		tag  int
	}{{"RSA", rpmutils.SIG_RSA}, {"DSA", rpmutils.SIG_DSA}, {"PGP", rpmutils.SIG_PGP}, {"GPG", rpmutils.SIG_GPG}} {
		if raw, e := hdr.GetBytes(item.tag); e == nil && len(raw) > 0 {
			sig, err := packet.Read(bytes.NewReader(raw))
			if err != nil {
				return Metadata{}, errors.New("invalid RPM package signature")
			}
			v4, ok := sig.(*packet.Signature)
			if !ok || v4.PubKeyAlgo != packet.PubKeyAlgoRSA || v4.Hash != crypto.SHA256 {
				return Metadata{}, errors.New("unsupported RPM package signature")
			}
			signatures = append(signatures, item.name)
		}
	}
	return Metadata{Identity: contract.Identity{Name: n.Name, Epoch: epoch, Version: n.Version, Release: n.Release, Architecture: n.Arch}, Format: "rpm", Fields: fields, Requires: requires, Requirements: dependencies, SignatureTags: signatures}, nil
}
