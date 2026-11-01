package rpm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

// Use real signatures so rejection cannot pass merely because the fixture
// contains placeholder MPIs or an untrusted key.
func TestUpstreamSignaturePolicy(t *testing.T) {
	for _, algorithm := range []string{"ed25519", "rsa2048", "nistp256"} {
		t.Run(algorithm, func(t *testing.T) {
			root, pub, cacheRoot, r, m, local := fixtureWithAlgorithm(t, algorithm)
			source := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
			raw := mustRead(t, source)
			header, err := rpmutils.ReadHeader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			span := header.GetRange()
			sign := func(data []byte) []byte {
				t.Helper()
				input, output := filepath.Join(root, "input"), filepath.Join(root, "signature")
				if err := os.WriteFile(input, data, 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("gpg", "--batch", "--yes", "--local-user", local.GPG.KeyID, "--digest-algo", "SHA512", "--detach-sign", "--output", output, input)
				cmd.Env = append(os.Environ(), "GNUPGHOME="+local.GPG.Home)
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("sign: %v: %s", err, b)
				}
				return mustRead(t, output)
			}
			headerSig, payloadSig := sign(raw[span.Start:span.End]), sign(raw[span.Start:])
			signed := filepath.Join(root, "upstream.rpm")
			in, err := os.Open(source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rpmutils.RewriteWithSignatures(in, signed, payloadSig, headerSig)
			_ = in.Close()
			if err != nil {
				t.Fatal(err)
			}
			if algorithm != "rsa2048" {
				if err := useEdDSASignatureTags(signed); err != nil {
					t.Fatal(err)
				}
			}
			raw = mustRead(t, signed)
			if tool, err := exec.LookPath("rpm"); err == nil {
				// Query the independent parser without requiring an installed
				// package keyring or changing the user's RPM database.
				out, err := exec.Command(tool, "--nosignature", "--query", "--package", "--queryformat", "%{NAME}", signed).CombinedOutput()
				if err != nil || string(out) != m.Packages[0].Identity.Name {
					t.Fatalf("rpm query: %v: %s", err, out)
				}
			}
			keys, err := openpgp.ReadKeyRing(bytes.NewReader(mustRead(t, pub)))
			if err != nil {
				t.Fatal(err)
			}
			_, sigs, err := rpmutils.Verify(bytes.NewReader(raw), keys)
			if err != nil || len(sigs) < 2 {
				t.Fatalf("upstream signatures did not verify: %v, %v", sigs, err)
			}
			meta, size, digest, err := inspect.File(signed, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyPackageSignature(signed, []string{pub}, "metadata-only"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyPackageSignature(signed, []string{pub}, "trusted-packages"); err == nil || !strings.Contains(err.Error(), "no trusted RSA or EdDSA SHA-256") {
				t.Fatalf("trusted policy accepted unsupported signature: %v", err)
			}
			sum := sha256.Sum256(raw)
			if digest != hex.EncodeToString(sum[:]) || size != int64(len(raw)) {
				t.Fatal("inspection changed package bytes")
			}
			p := &m.Packages[0]
			p.Identity, p.Size, p.SHA256 = meta.Identity, size, digest
			p.Path, err = contract.ServedPath(*p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", digest), raw, 0600); err != nil {
				t.Fatal(err)
			}
			site := filepath.Join(root, "site")
			if err := os.WriteFile(filepath.Join(site, p.Path), raw, 0644); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "rpm")
			// Repository signing still uses a supported key. ECDSA package
			// parsing and policy checks above do not widen repository signing.
			if algorithm == "nistp256" {
				return
			}
			if err := Build(out, cacheRoot, r, m, local); err != nil {
				t.Fatal(err)
			}
			if err := Verify(out, pub, site, r, m); err != nil {
				t.Fatal(err)
			}
			r.RPM.PackageSignature = "trusted-packages"
			r.RPM.TrustedKeyPaths = []string{"KEY.gpg"}
			rejected := filepath.Join(root, "rejected")
			if err := BuildWithKeyRoot(rejected, cacheRoot, root, r, m, local); err == nil || !strings.Contains(err.Error(), "no trusted RSA or EdDSA SHA-256") {
				t.Fatalf("trusted build accepted unsupported signature: %v", err)
			}
			if _, err := os.Stat(rejected); !os.IsNotExist(err) {
				t.Fatalf("failed build left output: %v", err)
			}
			if !bytes.Equal(raw, mustRead(t, signed)) || !bytes.Equal(raw, mustRead(t, filepath.Join(site, p.Path))) {
				t.Fatal("metadata build changed upstream bytes")
			}
		})
	}
}
