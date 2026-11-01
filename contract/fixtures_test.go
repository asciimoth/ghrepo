package contract_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/asciimoth/ghrepo/contract"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

type fixture struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Kind        string `json:"kind"`
	Want        string `json:"want"`
	SHA256      string `json:"sha256"`
	Compression string `json:"compression"`
}

func TestOfflineFixtures(t *testing.T) {
	catalog, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []fixture
	if err := json.Unmarshal(catalog, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 12 {
		t.Fatalf("fixture count = %d", len(cases))
	}
	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(tc.File)))
			if tc.Kind == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("want missing file: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.SHA256 != "" {
				sum := sha256.Sum256(data)
				matches := hex.EncodeToString(sum[:]) == tc.SHA256
				if matches != (tc.Kind != "hash-mismatch") {
					t.Fatalf("hash result is wrong: %v", matches)
				}
			}
			switch tc.Kind {
			case "deb", "hash-mismatch", "malformed", "unsafe-path", "duplicate-identity":
				if tc.Kind == "malformed" {
					if err := contract.CheckDEBFormat(data); !errors.Is(err, contract.ErrMalformedDEB) {
						t.Fatalf("DEB format error: %v", err)
					}
				} else if err := contract.CheckDEBFormat(data); err != nil {
					t.Fatal(err)
				}
				identity, err := readDebControl(data)
				switch tc.Kind {
				case "malformed", "unsafe-path":
					if err == nil {
						t.Fatal("want archive error")
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
					if tc.Want != "" && identity != tc.Want {
						t.Fatalf("identity = %q", identity)
					}
					if tc.Kind == "duplicate-identity" && seen[identity] == hexSHA(data) {
						t.Fatal("duplicate fixture must have changed bytes")
					}
					if tc.Kind == "deb" {
						seen[identity] = hexSHA(data)
					}
				}
			case "rpm":
				if err := contract.CheckRPMFormat(data); err != nil {
					t.Fatal(err)
				}
				rpm, err := rpmutils.ReadRpm(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				payload, err := rpm.PayloadReaderExtended()
				if err != nil {
					t.Fatal(err)
				}
				for {
					_, err := payload.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err := io.Copy(io.Discard, payload); err != nil {
						t.Fatal(err)
					}
				}
				if len(data) < 97 || data[4] != 3 {
					t.Fatal("expected RPM lead version 3")
				}
				hdr, err := rpmutils.ReadHeader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				nevra, err := hdr.GetNEVRA()
				if err != nil {
					t.Fatal(err)
				}
				got := fmt.Sprintf("%s %s:%s-%s %s", nevra.Name, nevra.Epoch, nevra.Version, nevra.Release, nevra.Arch)
				if got != tc.Want {
					t.Fatalf("RPM identity = %q, want %q", got, tc.Want)
				}
				if tc.Compression != "" {
					got, err := hdr.GetString(rpmutils.PAYLOADCOMPRESSOR)
					if err != nil {
						t.Fatal(err)
					}
					if got != tc.Compression {
						t.Fatalf("RPM compression = %q, want %q", got, tc.Compression)
					}
				}
			case "invalid-signature":
				if !bytes.Contains(data, []byte("BEGIN PGP SIGNATURE")) {
					t.Fatal("not an armored signature fixture")
				}
				block, err := armor.Decode(bytes.NewReader(data))
				if err == nil {
					_, err = packet.Read(block.Body)
				}
				if err == nil {
					t.Fatal("invalid OpenPGP signature accepted")
				}
			default:
				t.Fatalf("unknown fixture kind %q", tc.Kind)
			}
		})
	}
}

func hexSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestUnsupportedFormats(t *testing.T) {
	deb, err := os.ReadFile("testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	badDeb := bytes.Replace(deb, []byte("control.tar.gz"), []byte("control.tar.xz"), 1)
	if err := contract.CheckDEBFormat(badDeb); !errors.Is(err, contract.ErrUnsupportedDEBCompression) {
		t.Fatalf("DEB compression error = %v", err)
	}
	rpm, err := os.ReadFile("testdata/packages/empty-0.1-1.x86_64.rpm")
	if err != nil {
		t.Fatal(err)
	}
	rpm[4] = 6
	if err := contract.CheckRPMFormat(rpm); !errors.Is(err, contract.ErrUnsupportedRPMVersion) {
		t.Fatalf("RPM version error = %v", err)
	}
}

// This small reader checks fixture shape. Production package inspection is stage 2.
func readDebControl(b []byte) (string, error) {
	if len(b) < 8 || string(b[:8]) != "!<arch>\n" {
		return "", errors.New("invalid ar magic")
	}
	b = b[8:]
	for len(b) >= 60 {
		name := strings.TrimSuffix(strings.TrimSpace(string(b[:16])), "/")
		if string(b[58:60]) != "`\n" {
			return "", errors.New("invalid ar header")
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b[48:58])))
		if err != nil || n < 0 || n > len(b)-60 {
			return "", errors.New("invalid ar member size")
		}
		body := b[60 : 60+n]
		if name == "control.tar.gz" {
			gz, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				return "", err
			}
			tr := tar.NewReader(gz)
			for {
				h, err := tr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return "", err
				}
				if path.IsAbs(h.Name) || strings.HasPrefix(h.Name, "../") || strings.Contains(h.Name, "/../") {
					return "", errors.New("unsafe control path")
				}
				if h.Name == "control" || h.Name == "./control" {
					fields, err := io.ReadAll(tr)
					if err != nil {
						return "", err
					}
					m := map[string]string{}
					for _, line := range strings.Split(string(fields), "\n") {
						k, v, ok := strings.Cut(line, ": ")
						if ok {
							m[k] = v
						}
					}
					return m["Package"] + " " + m["Version"] + " " + m["Architecture"], nil
				}
			}
			return "", errors.New("missing control file")
		}
		b = b[60+n:]
		if n%2 != 0 {
			b = b[1:]
		}
	}
	return "", errors.New("missing control archive")
}
