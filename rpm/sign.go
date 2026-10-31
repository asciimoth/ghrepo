package rpm

import (
	"bytes"
	"crypto"
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
	rsa, e := signBytes(raw[span.Start:span.End], local)
	if e != nil {
		return e
	}
	pgp, e := signBytes(raw[span.Start:], local)
	if e != nil {
		return e
	}
	for _, sig := range [][]byte{rsa, pgp} {
		pkt, e := packet.Read(bytes.NewReader(sig))
		if e != nil {
			return e
		}
		v, ok := pkt.(*packet.Signature)
		if !ok || v.PubKeyAlgo != packet.PubKeyAlgoRSA || v.Hash != crypto.SHA256 {
			return errors.New("unsupported RPM package signature algorithm")
		}
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
	if _, e = rpmutils.RewriteWithSignatures(in, tmpPath, pgp, rsa); e != nil {
		return fmt.Errorf("write signed RPM: %w", e)
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
