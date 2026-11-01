package rpm

import (
	"bytes"
	"crypto"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

// SignCopy signs a supported RPM with the configured local GPG key. It writes
// only to a new path. The caller must inspect and hash the final bytes before
// adding the copy to the manifest.
func SignCopy(source, destination string, local contract.Local, trustedKeys []string) error {
	if source == destination {
		return errors.New("RPM signing requires a new output path")
	}
	if local.GPG.KeyID == "" {
		return errors.New("RPM signing key is not configured")
	}
	if _, e := os.Lstat(destination); e == nil {
		return errors.New("RPM signed copy already exists")
	} else if !os.IsNotExist(e) {
		return e
	}
	raw, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	original, e := inspect.Bytes(raw)
	if e != nil {
		return e
	}
	if original.Format != "rpm" {
		return errors.New("RPM signing requires an RPM package")
	}
	hdr, e := rpmutils.ReadHeader(bytes.NewReader(raw))
	if e != nil {
		return e
	}
	span := hdr.GetRange()
	if span.Start < 96 || span.End < span.Start || span.End > len(raw) {
		return errors.New("invalid RPM header range")
	}
	// gpg signs the general header and the general header plus payload. No
	// private key material leaves the selected local keyring or signing agent.
	headerSignature, e := signBytes(raw[span.Start:span.End], local)
	if e != nil {
		return e
	}
	payloadSignature, e := signBytes(raw[span.Start:], local)
	if e != nil {
		return e
	}
	var algorithm packet.PublicKeyAlgorithm
	for _, sig := range [][]byte{headerSignature, payloadSignature} {
		pkt, e := packet.Read(bytes.NewReader(sig))
		if e != nil {
			return e
		}
		v, ok := pkt.(*packet.Signature)
		if !ok || (v.PubKeyAlgo != packet.PubKeyAlgoRSA && v.PubKeyAlgo != packet.PubKeyAlgoEdDSA) || v.Hash != crypto.SHA256 || (algorithm != 0 && v.PubKeyAlgo != algorithm) {
			return errors.New("unsupported RPM package signature algorithm")
		}
		algorithm = v.PubKeyAlgo
	}
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer func() { _ = in.Close() }()
	tmp, e := os.CreateTemp(filepath.Dir(destination), ".ghrepo-signed-*")
	if e != nil {
		return e
	}
	tmpPath := tmp.Name()
	if e := tmp.Close(); e != nil {
		_ = os.Remove(tmpPath)
		return e
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if _, e = rpmutils.RewriteWithSignatures(in, tmpPath, payloadSignature, headerSignature); e != nil {
		return fmt.Errorf("write signed RPM: %w", e)
	}
	if algorithm == packet.PubKeyAlgoEdDSA {
		if e = useEdDSASignatureTags(tmpPath); e != nil {
			return e
		}
	}
	final, _, _, e := inspect.File(tmpPath, int64(len(raw))+1<<20)
	if e != nil {
		return e
	}
	if final.Identity != original.Identity || final.Format != "rpm" {
		return errors.New("signed RPM identity changed")
	}
	if e = VerifyPackageSignature(tmpPath, trustedKeys, "trusted-packages"); e != nil {
		return e
	}
	// Link does not replace an existing destination. Removing the temporary
	// name leaves one complete regular file at the requested path.
	if e = os.Link(tmpPath, destination); e != nil {
		return fmt.Errorf("publish signed RPM: %w", e)
	}
	if e = os.Remove(tmpPath); e != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("finish signed RPM: %w", e)
	}
	return nil
}

// RewriteWithSignatures writes RSA tag numbers. RPM stores EdDSA signatures
// under the historical DSA and GPG tag numbers. Change only those index tags;
// the OpenPGP packets and signed package bytes stay the same.
func useEdDSASignatureTags(name string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if len(data) < 112 || binary.BigEndian.Uint32(data[96:100]) != 0x8eade801 {
		return errors.New("invalid RPM signature header")
	}
	count := uint64(binary.BigEndian.Uint32(data[104:108]))
	if count > uint64(len(data)-112)/16 {
		return errors.New("invalid RPM signature index")
	}
	headerTags, payloadTags := 0, 0
	for i := uint64(0); i < count; i++ {
		offset := 112 + int(i)*16
		switch binary.BigEndian.Uint32(data[offset : offset+4]) {
		case 268: // RSAHEADER
			binary.BigEndian.PutUint32(data[offset:offset+4], 267) // DSAHEADER
			headerTags++
		case 1002: // PGP
			binary.BigEndian.PutUint32(data[offset:offset+4], 1005) // GPG
			payloadTags++
		case 267, 1005:
			return errors.New("duplicate RPM signature tag")
		}
	}
	if headerTags != 1 || payloadTags != 1 {
		return errors.New("missing RPM signature tag")
	}
	return os.WriteFile(name, data, 0600)
}

func signBytes(data []byte, local contract.Local) ([]byte, error) {
	dir, err := os.MkdirTemp("", "ghrepo-rpm-sign-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "signature")
	if err := os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	args := []string{"--batch", "--yes", "--local-user", local.GPG.KeyID, "--digest-algo", "SHA256", "--detach-sign", "--output", output, input}
	if local.GPG.Keyring != "" {
		args = append([]string{"--no-default-keyring", "--keyring", local.GPG.Keyring}, args...)
	}
	cmd := exec.Command("gpg", args...)
	if local.GPG.Home != "" {
		cmd.Env = append(os.Environ(), "GNUPGHOME="+local.GPG.Home)
	}
	if err := cmd.Run(); err != nil {
		return nil, errors.New("RPM package signing failed")
	}
	return os.ReadFile(output)
}
