// Package rpm builds and verifies version 1 RPM repository metadata.
package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

const commonNS = "http://linux.duke.edu/metadata/common"
const repoNS = "http://linux.duke.edu/metadata/repo"
const fileNS = "http://linux.duke.edu/metadata/filelists"
const otherNS = "http://linux.duke.edu/metadata/other"
const rpmNS = "http://linux.duke.edu/metadata/rpm"
const maxMetadataBytes = 128 << 20

type checksum struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}
type location struct {
	Href string `xml:"href,attr"`
}
type version struct {
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
}
type size struct {
	Package   int64 `xml:"package,attr"`
	Installed int64 `xml:"installed,attr"`
	Archive   int64 `xml:"archive,attr"`
}
type timestamp struct {
	File  int64 `xml:"file,attr"`
	Build int64 `xml:"build,attr"`
}
type dependency struct {
	Name  string `xml:"name,attr"`
	Flags string `xml:"flags,attr,omitempty"`
	Epoch string `xml:"epoch,attr,omitempty"`
	Ver   string `xml:"ver,attr,omitempty"`
	Rel   string `xml:"rel,attr,omitempty"`
}
type dependencyList struct {
	Entries []dependency `xml:"entry"`
}
type format struct {
	XMLNS     string         `xml:"xmlns:rpm,attr"`
	License   string         `xml:"http://linux.duke.edu/metadata/rpm license"`
	Requires  dependencyList `xml:"http://linux.duke.edu/metadata/rpm requires"`
	Provides  dependencyList `xml:"http://linux.duke.edu/metadata/rpm provides"`
	Conflicts dependencyList `xml:"http://linux.duke.edu/metadata/rpm conflicts"`
	Obsoletes dependencyList `xml:"http://linux.duke.edu/metadata/rpm obsoletes"`
}
type primaryPackage struct {
	Type        string    `xml:"type,attr"`
	Name        string    `xml:"name"`
	Arch        string    `xml:"arch"`
	Version     version   `xml:"version"`
	Checksum    checksum  `xml:"checksum"`
	Summary     string    `xml:"summary"`
	Description string    `xml:"description"`
	Packager    string    `xml:"packager"`
	URL         string    `xml:"url"`
	Time        timestamp `xml:"time"`
	Size        size      `xml:"size"`
	Location    location  `xml:"location"`
	Format      format    `xml:"format"`
}
type primary struct {
	XMLName  xml.Name         `xml:"metadata"`
	XMLNS    string           `xml:"xmlns,attr"`
	Packages int              `xml:"packages,attr"`
	Entries  []primaryPackage `xml:"package"`
}
type fileEntry struct {
	Type string `xml:"type,attr,omitempty"`
	Path string `xml:",chardata"`
}
type filePackage struct {
	PkgID   string      `xml:"pkgid,attr"`
	Name    string      `xml:"name,attr"`
	Arch    string      `xml:"arch,attr"`
	Version version     `xml:"version"`
	Files   []fileEntry `xml:"file"`
}
type filelists struct {
	XMLName  xml.Name      `xml:"filelists"`
	XMLNS    string        `xml:"xmlns,attr"`
	Packages int           `xml:"packages,attr"`
	Entries  []filePackage `xml:"package"`
}
type otherPackage struct {
	PkgID   string  `xml:"pkgid,attr"`
	Name    string  `xml:"name,attr"`
	Arch    string  `xml:"arch,attr"`
	Version version `xml:"version"`
}
type other struct {
	XMLName  xml.Name       `xml:"otherdata"`
	XMLNS    string         `xml:"xmlns,attr"`
	Packages int            `xml:"packages,attr"`
	Entries  []otherPackage `xml:"package"`
}
type repodata struct {
	Type         string   `xml:"type,attr"`
	Checksum     checksum `xml:"checksum"`
	OpenChecksum checksum `xml:"open-checksum"`
	Location     location `xml:"location"`
	Timestamp    int64    `xml:"timestamp"`
	Size         int      `xml:"size"`
	OpenSize     int      `xml:"open-size"`
}
type repomd struct {
	XMLName  xml.Name   `xml:"repomd"`
	XMLNS    string     `xml:"xmlns,attr"`
	Revision int64      `xml:"revision"`
	Data     []repodata `xml:"data"`
}

func marshal(v any) ([]byte, error) {
	b, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(b, '\n')...), nil
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func readMetadataFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid RPM metadata file type or size")
	}
	return os.ReadFile(name)
}
func gzipData(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.ModTime = time.Unix(0, 0)
	w.OS = 255
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Build writes a complete signed rpm directory in a new output path.
func Build(out, cacheRoot string, r contract.Repository, m contract.Manifest, local contract.Local) (buildErr error) {
	return BuildWithKeyRoot(out, cacheRoot, ".", r, m, local)
}

// BuildWithKeyRoot resolves trusted package key paths from the checkout root.
func BuildWithKeyRoot(out, cacheRoot, keyRoot string, r contract.Repository, m contract.Manifest, local contract.Local) (buildErr error) {
	if err := m.Validate(r); err != nil {
		return err
	}
	if local.GPG.KeyID == "" {
		return errors.New("RPM metadata signing key is not configured")
	}
	if _, err := os.Lstat(out); err == nil {
		return errors.New("RPM output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "repodata"), 0755); err != nil {
		return err
	}
	defer func() {
		if buildErr != nil {
			_ = os.RemoveAll(out)
		}
	}()
	entries := active(m)
	now := time.Now().Unix()
	pdoc := primary{XMLNS: commonNS, Packages: len(entries)}
	fdoc := filelists{XMLNS: fileNS, Packages: len(entries)}
	odoc := other{XMLNS: otherNS, Packages: len(entries)}
	for _, p := range entries {
		name := filepath.Join(cacheRoot, "sha256", p.SHA256)
		if err := cache.VerifyFile(name, p); err != nil {
			return fmt.Errorf("package %s: %w", p.Path, err)
		}
		meta, n, digest, err := inspect.File(name, r.Downloads.MaxBytes)
		if err != nil {
			return err
		}
		if meta.Format != "rpm" || meta.Identity != p.Identity || n != p.Size || digest != p.SHA256 {
			return fmt.Errorf("package %s: inspected identity differs", p.Path)
		}
		keys := make([]string, len(r.RPM.TrustedKeyPaths))
		for i, key := range r.RPM.TrustedKeyPaths {
			keys[i] = filepath.Join(keyRoot, filepath.FromSlash(key))
		}
		if err := VerifyPackageSignature(name, keys, r.RPM.PackageSignature); err != nil {
			return fmt.Errorf("package %s: %w", p.Path, err)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		hdr, err := rpmutils.ReadHeader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		installed, err := hdr.InstalledSize()
		if err != nil {
			return err
		}
		archive, err := hdr.PayloadSize()
		if err != nil {
			archive = 0
		}
		v := version{p.Identity.Epoch, p.Identity.Version, p.Identity.Release}
		requires, err := headerDependencies(hdr, rpmutils.REQUIRENAME, rpmutils.REQUIREVERSION, rpmutils.REQUIREFLAGS)
		if err != nil {
			return err
		}
		provides, err := headerDependencies(hdr, rpmutils.PROVIDENAME, rpmutils.PROVIDEVERSION, rpmutils.PROVIDEFLAGS)
		if err != nil {
			return err
		}
		conflicts, err := headerDependencies(hdr, rpmutils.CONFLICTNAME, rpmutils.CONFLICTVERSION, rpmutils.CONFLICTFLAGS)
		if err != nil {
			return err
		}
		obsoletes, err := headerDependencies(hdr, rpmutils.OBSOLETENAME, rpmutils.OBSOLETEVERSION, rpmutils.OBSOLETEFLAGS)
		if err != nil {
			return err
		}
		files, err := hdr.GetFiles()
		if err != nil {
			return err
		}
		fileEntries := make([]fileEntry, 0, len(files))
		for _, f := range files {
			if !strings.HasPrefix(f.Name(), "/") || strings.Contains(f.Name(), "..") {
				return errors.New("invalid RPM file path")
			}
			item := fileEntry{Path: f.Name()}
			if f.Mode()&0170000 == 0040000 {
				item.Type = "dir"
			}
			fileEntries = append(fileEntries, item)
		}
		pdoc.Entries = append(pdoc.Entries, primaryPackage{Type: "rpm", Name: p.Identity.Name, Arch: p.Identity.Architecture, Version: v, Checksum: checksum{"sha256", p.SHA256}, Summary: meta.Fields["Summary"], Description: meta.Fields["Description"], URL: meta.Fields["URL"], Time: timestamp{0, 0}, Size: size{p.Size, installed, archive}, Location: location{strings.TrimPrefix(p.Path, "rpm/")}, Format: format{XMLNS: rpmNS, License: meta.Fields["License"], Requires: requires, Provides: provides, Conflicts: conflicts, Obsoletes: obsoletes}})
		fdoc.Entries = append(fdoc.Entries, filePackage{PkgID: p.SHA256, Name: p.Identity.Name, Arch: p.Identity.Architecture, Version: v, Files: fileEntries})
		odoc.Entries = append(odoc.Entries, otherPackage{p.SHA256, p.Identity.Name, p.Identity.Architecture, v})
	}
	docs := []struct {
		kind  string
		value any
	}{{"primary", pdoc}, {"filelists", fdoc}, {"other", odoc}}
	md := repomd{XMLNS: repoNS, Revision: now}
	for _, doc := range docs {
		plain, err := marshal(doc.value)
		if err != nil {
			return err
		}
		zipped, err := gzipData(plain)
		if err != nil {
			return err
		}
		file := hash(zipped) + "-" + doc.kind + ".xml.gz"
		if err := os.WriteFile(filepath.Join(out, "repodata", file), zipped, 0644); err != nil {
			return err
		}
		md.Data = append(md.Data, repodata{Type: doc.kind, Checksum: checksum{"sha256", hash(zipped)}, OpenChecksum: checksum{"sha256", hash(plain)}, Location: location{"repodata/" + file}, Timestamp: now, Size: len(zipped), OpenSize: len(plain)})
	}
	data, err := marshal(md)
	if err != nil {
		return err
	}
	repopath := filepath.Join(out, "repodata", "repomd.xml")
	if err := os.WriteFile(repopath, data, 0644); err != nil {
		return err
	}
	args := []string{"--batch", "--yes", "--local-user", local.GPG.KeyID, "--digest-algo", "SHA256", "--armor", "--detach-sign", "--output", repopath + ".asc", repopath}
	if local.GPG.Keyring != "" {
		args = append([]string{"--no-default-keyring", "--keyring", local.GPG.Keyring}, args...)
	}
	cmd := exec.Command("gpg", args...)
	if local.GPG.Home != "" {
		cmd.Env = append(os.Environ(), "GNUPGHOME="+local.GPG.Home)
	}
	if err := cmd.Run(); err != nil {
		return errors.New("RPM metadata signing failed")
	}
	return nil
}
func active(m contract.Manifest) []contract.Package {
	var a []contract.Package
	for _, p := range m.Packages {
		if p.Format == "rpm" && p.State == "active" {
			a = append(a, p)
		}
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Path < a[j].Path })
	return a
}
func headerDependencies(hdr *rpmutils.RpmHeader, namesTag, versionsTag, flagsTag int) (dependencyList, error) {
	names, e := hdr.GetStrings(namesTag)
	if e != nil {
		var absent rpmutils.NoSuchTagError
		if errors.As(e, &absent) {
			return dependencyList{}, nil
		}
		return dependencyList{}, e
	}
	versions, e := hdr.GetStrings(versionsTag)
	if e != nil {
		return dependencyList{}, e
	}
	flags, e := hdr.GetUint32s(flagsTag)
	if e != nil {
		return dependencyList{}, e
	}
	if len(names) != len(versions) || len(names) != len(flags) {
		return dependencyList{}, errors.New("invalid RPM dependency fields")
	}
	out := dependencyList{}
	for i, name := range names {
		d := dependency{Name: name, Flags: dependencyFlags(flags[i])}
		if versions[i] != "" {
			evr := versions[i]
			if epoch, rest, ok := strings.Cut(evr, ":"); ok {
				d.Epoch = epoch
				evr = rest
			}
			if ver, rel, ok := strings.Cut(evr, "-"); ok {
				d.Ver = ver
				d.Rel = rel
			} else {
				d.Ver = evr
			}
		}
		out.Entries = append(out.Entries, d)
	}
	return out, nil
}
func dependencyFlags(v uint32) string {
	switch v & 0xe {
	case 0x2:
		return "LT"
	case 0x4:
		return "GT"
	case 0x8:
		return "EQ"
	case 0xa:
		return "LE"
	case 0xc:
		return "GE"
	}
	return ""
}

// VerifyPackageSignature enforces the package policy independently of repomd signing.
func VerifyPackageSignature(name string, keyPaths []string, policy string) error {
	if policy == "metadata-only" {
		return nil
	}
	if policy != "trusted-packages" {
		return errors.New("unknown RPM package signature policy")
	}
	var keys openpgp.EntityList
	for _, path := range keyPaths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		k, e := openpgp.ReadKeyRing(f)
		_ = f.Close()
		if e != nil {
			f, e = os.Open(path)
			if e != nil {
				return e
			}
			k, e = openpgp.ReadArmoredKeyRing(f)
			_ = f.Close()
			if e != nil {
				return e
			}
		}
		keys = append(keys, k...)
	}
	if len(keys) == 0 {
		return errors.New("no trusted RPM package keys")
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, sigs, err := rpmutils.Verify(f, keys)
	if err != nil {
		return fmt.Errorf("RPM package signature verification failed: %w", err)
	}
	for _, sig := range sigs {
		if sig.Signer != nil && !sig.HeaderOnly && sig.Hash == crypto.SHA256 && signatureUsesSupportedAlgorithm(sig) {
			return nil
		}
	}
	return errors.New("RPM package has no trusted RSA or EdDSA SHA-256 signature")
}

func signatureUsesSupportedAlgorithm(sig *rpmutils.Signature) bool {
	if key := sig.Signer.PrimaryKey; key != nil && key.KeyId == sig.KeyId {
		return key.PubKeyAlgo == packet.PubKeyAlgoRSA || key.PubKeyAlgo == packet.PubKeyAlgoEdDSA
	}
	for _, subkey := range sig.Signer.Subkeys {
		if key := subkey.PublicKey; key != nil && key.KeyId == sig.KeyId {
			return key.PubKeyAlgo == packet.PubKeyAlgoRSA || key.PubKeyAlgo == packet.PubKeyAlgoEdDSA
		}
	}
	return false
}

// Verify checks the detached signature, all metadata links, and package bytes.
// packageRoot is the site root containing the paths in the manifest.
func Verify(dir, publicKey, packageRoot string, r contract.Repository, m contract.Manifest) error {
	if err := m.Validate(r); err != nil {
		return err
	}
	var err error
	publicKey, err = filepath.Abs(publicKey)
	if err != nil {
		return err
	}
	repopath := filepath.Join(dir, "repodata", "repomd.xml")
	cmd := exec.Command("gpgv", "--status-fd", "1", "--keyring", publicKey, repopath+".asc", repopath)
	status, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("RPM repomd signature verification failed: %w: %s", err, strings.TrimSpace(string(status)))
	}
	good := false
	for _, line := range strings.Split(string(status), "\n") {
		p := strings.Fields(line)
		if len(p) >= 10 && p[0] == "[GNUPG:]" && p[1] == "VALIDSIG" && p[9] == "8" && (p[8] == "1" || p[8] == "2" || p[8] == "3" || p[8] == "22") {
			good = true
		}
	}
	if !good {
		return errors.New("unsupported RPM repository signature algorithm")
	}
	raw, err := readMetadataFile(repopath, 1<<20)
	if err != nil {
		return err
	}
	var md repomd
	if err := xml.Unmarshal(raw, &md); err != nil {
		return err
	}
	if md.XMLNS != repoNS || len(md.Data) != 3 {
		return errors.New("invalid RPM repomd")
	}
	docs := map[string][]byte{}
	seen := map[string]bool{}
	for _, item := range md.Data {
		if item.Type != "primary" && item.Type != "filelists" && item.Type != "other" || seen[item.Type] {
			return errors.New("unsupported or duplicate RPM metadata type")
		}
		seen[item.Type] = true
		if err := contract.SafePath(item.Location.Href); err != nil {
			return err
		}
		if !strings.HasPrefix(item.Location.Href, "repodata/") || !strings.HasSuffix(item.Location.Href, "-"+item.Type+".xml.gz") {
			return errors.New("invalid RPM metadata path")
		}
		filename := filepath.Join(dir, filepath.FromSlash(item.Location.Href))
		b, err := readMetadataFile(filename, maxMetadataBytes)
		if err != nil {
			return err
		}
		if item.Checksum.Type != "sha256" || item.Checksum.Value != hash(b) || item.Size != len(b) || filepath.Base(filename) != item.Checksum.Value+"-"+item.Type+".xml.gz" {
			return errors.New("RPM metadata checksum mismatch")
		}
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return err
		}
		plain, err := io.ReadAll(io.LimitReader(zr, maxMetadataBytes+1))
		if err != nil {
			return err
		}
		if err := zr.Close(); err != nil {
			return err
		}
		if len(plain) > maxMetadataBytes || item.OpenChecksum.Type != "sha256" || item.OpenChecksum.Value != hash(plain) || item.OpenSize != len(plain) {
			return errors.New("RPM open metadata checksum mismatch")
		}
		docs[item.Type] = plain
	}
	expected := active(m)
	want := map[string]contract.Package{}
	for _, p := range expected {
		want[p.Path] = p
	}
	var pdoc primary
	if err := xml.Unmarshal(docs["primary"], &pdoc); err != nil {
		return err
	}
	if pdoc.XMLNS != commonNS || pdoc.Packages != len(expected) || len(pdoc.Entries) != len(expected) {
		return errors.New("RPM primary package count mismatch")
	}
	ids := map[string]bool{}
	for _, e := range pdoc.Entries {
		if err := contract.SafePath(e.Location.Href); err != nil {
			return err
		}
		p, ok := want["rpm/"+e.Location.Href]
		if !ok || ids[p.Path] || e.Type != "rpm" || e.Name != p.Identity.Name || e.Arch != p.Identity.Architecture || e.Version != (version{p.Identity.Epoch, p.Identity.Version, p.Identity.Release}) || e.Checksum != (checksum{"sha256", p.SHA256}) || e.Size.Package != p.Size {
			return fmt.Errorf("RPM primary package mismatch: %s", e.Location.Href)
		}
		ids[p.Path] = true
		if packageRoot != "" {
			name := filepath.Join(packageRoot, filepath.FromSlash(p.Path))
			if err := cache.VerifyFile(name, p); err != nil {
				return err
			}
			keys := make([]string, len(r.RPM.TrustedKeyPaths))
			for i, key := range r.RPM.TrustedKeyPaths {
				keys[i] = filepath.Join(packageRoot, filepath.FromSlash(key))
			}
			if err := VerifyPackageSignature(name, keys, r.RPM.PackageSignature); err != nil {
				return err
			}
		}
	}
	for _, kind := range []string{"filelists", "other"} {
		var entries []otherPackage
		var count int
		var namespace string
		if kind == "filelists" {
			var d filelists
			if err := xml.Unmarshal(docs[kind], &d); err != nil {
				return err
			}
			count, namespace = d.Packages, d.XMLNS
			for _, e := range d.Entries {
				entries = append(entries, otherPackage{PkgID: e.PkgID, Name: e.Name, Arch: e.Arch, Version: e.Version})
			}
		} else {
			var d other
			if err := xml.Unmarshal(docs[kind], &d); err != nil {
				return err
			}
			count, namespace, entries = d.Packages, d.XMLNS, d.Entries
		}
		if count != len(expected) || len(entries) != len(expected) || (kind == "filelists" && namespace != fileNS) || (kind == "other" && namespace != otherNS) {
			return errors.New("RPM auxiliary metadata count mismatch")
		}
		found := map[string]bool{}
		for _, e := range entries {
			p, ok := wantPathID(want, e.PkgID)
			if !ok || found[e.PkgID] || e.Name != p.Identity.Name || e.Arch != p.Identity.Architecture || e.Version != (version{p.Identity.Epoch, p.Identity.Version, p.Identity.Release}) {
				return errors.New("RPM auxiliary package mismatch")
			}
			found[e.PkgID] = true
		}
	}
	return nil
}
func wantPathID(want map[string]contract.Package, id string) (contract.Package, bool) {
	for _, p := range want {
		if p.SHA256 == id {
			return p, true
		}
	}
	return contract.Package{}, false
}
