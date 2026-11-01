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
				if hdr.Name == "" || strings.HasPrefix(hdr.Name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains("/"+hdr.Name+"/", "/../") || (clean == "." && hdr.Typeflag != tar.TypeDir) {
					return Metadata{}, errors.New("unsafe or unsupported DEB control path")
				}
				if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir) || (hdr.Typeflag == tar.TypeDir && hdr.Size != 0) {
					return Metadata{}, errors.New("unsafe or unsupported DEB control path")
				}
				if clean == "control" {
					if (hdr.Name != "control" && hdr.Name != "./control") || hdr.Typeflag != tar.TypeReg || found || hdr.Size > 1024*1024 || hdr.Size < 1 {
						return Metadata{}, errors.New("unsafe or unsupported DEB control path")
					}
					control, err = io.ReadAll(io.LimitReader(tr, 1024*1024+1))
					if err != nil {
						return Metadata{}, fmt.Errorf("invalid DEB control member: %w", err)
					}
					found = true
				} else if _, err := io.Copy(io.Discard, tr); err != nil {
					return Metadata{}, fmt.Errorf("invalid DEB control member: %w", err)
				}
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
	finished := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.ContainsAny(line, "\r\x00") {
			return Metadata{}, errors.New("invalid DEB control field value")
		}
		if strings.Trim(line, " \t") == "" {
			if len(fields) != 0 {
				finished = true
			}
			continue
		}
		if finished {
			return Metadata{}, errors.New("multiple DEB control stanzas")
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if previous == "" {
				return Metadata{}, errors.New("invalid DEB control continuation")
			}
			fields[previous] += "\n" + line
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		lower := strings.ToLower(key)
		if !ok || key == "" || seen[lower] {
			return Metadata{}, errors.New("invalid or duplicate DEB control field")
		}
		if key[0] == '#' || key[0] == '-' {
			return Metadata{}, errors.New("invalid DEB control field")
		}
		for _, c := range key {
			if c < '!' || c > '~' {
				return Metadata{}, errors.New("invalid DEB control field")
			}
		}
		seen[lower] = true
		if standard, ok := canonical[lower]; ok {
			key = standard
		}
		fields[key] = strings.Trim(value, " \t")
		previous = key
	}
	if scanner.Err() != nil {
		return Metadata{}, scanner.Err()
	}
	for key, value := range fields {
		if strings.Trim(value, " \t\n") == "" {
			return Metadata{}, fmt.Errorf("empty DEB control field %s", key)
		}
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
	for _, tag := range []int{rpmutils.NAME, rpmutils.VERSION, rpmutils.RELEASE, rpmutils.ARCH} {
		if value, err := hdr.GetString(tag); err != nil || value == "" {
			return Metadata{}, errors.New("invalid RPM identity field")
		}
	}
	if hdr.HasTag(rpmutils.EPOCH) {
		if values, err := hdr.GetUint64s(rpmutils.EPOCH); err != nil || len(values) != 1 {
			return Metadata{}, errors.New("invalid RPM epoch")
		}
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
		if !hdr.HasTag(tag) {
			continue
		}
		values, err := hdr.GetStrings(tag)
		if err != nil || len(values) == 0 || ((key == "License" || key == "URL") && len(values) != 1) {
			return Metadata{}, fmt.Errorf("invalid RPM %s field", key)
		}
		// RPM stores the default (C locale) text first, followed by translations.
		fields[key] = values[0]
	}
	var requires, versions []string
	var flags []uint32
	if hdr.HasTag(rpmutils.REQUIRENAME) || hdr.HasTag(rpmutils.REQUIREVERSION) || hdr.HasTag(rpmutils.REQUIREFLAGS) {
		var namesErr, versionsErr, flagsErr error
		requires, namesErr = hdr.GetStrings(rpmutils.REQUIRENAME)
		versions, versionsErr = hdr.GetStrings(rpmutils.REQUIREVERSION)
		flags, flagsErr = hdr.GetUint32s(rpmutils.REQUIREFLAGS)
		if namesErr != nil || versionsErr != nil || flagsErr != nil || len(versions) != len(requires) || len(flags) != len(requires) {
			return Metadata{}, errors.New("invalid RPM dependency fields")
		}
	}
	dependencies := make([]Requirement, len(requires))
	for i, name := range requires {
		dependencies[i] = Requirement{Name: name, Version: versions[i], Flags: flags[i]}
	}
	var signatures []string
	for _, item := range []struct {
		name string
		tag  int
		algo packet.PublicKeyAlgorithm
	}{{"RSA", rpmutils.SIG_RSA, packet.PubKeyAlgoRSA}, {"DSA", rpmutils.SIG_DSA, packet.PubKeyAlgoEdDSA}, {"PGP", rpmutils.SIG_PGP, packet.PubKeyAlgoRSA}, {"GPG", rpmutils.SIG_GPG, packet.PubKeyAlgoEdDSA}} {
		if hdr.HasTag(item.tag) {
			raw, err := hdr.GetBytes(item.tag)
			if err != nil || len(raw) == 0 {
				return Metadata{}, errors.New("invalid RPM package signature tag")
			}
			reader := bytes.NewReader(raw)
			sig, err := packet.Read(reader)
			if err != nil || reader.Len() != 0 {
				return Metadata{}, errors.New("invalid RPM package signature")
			}
			v4, ok := sig.(*packet.Signature)
			if !ok || v4.PubKeyAlgo != item.algo || v4.Hash != crypto.SHA256 {
				return Metadata{}, errors.New("unsupported RPM package signature")
			}
			signatures = append(signatures, item.name)
		}
	}
	return Metadata{Identity: contract.Identity{Name: n.Name, Epoch: epoch, Version: n.Version, Release: n.Release, Architecture: n.Arch}, Format: "rpm", Fields: fields, Requires: requires, Requirements: dependencies, SignatureTags: signatures}, nil
}
