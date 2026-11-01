package inspect

import (
	"archive/tar"
	"bytes"
	"fmt"
	"testing"

	rpmutils "github.com/sassoftware/go-rpmutils"
)

// These packet bytes are parseable OpenPGP v4 signature packets. Their MPI
// values are placeholders; package inspection parses signature metadata and
// does not claim to verify a signature with a trusted key.
var (
	rsaPacket   = []byte{0xc2, 19, 4, 0, 1, 8, 0, 6, 5, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 1}
	eddsaPacket = []byte{0xc2, 22, 4, 0, 22, 8, 0, 6, 5, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 1, 0, 1, 1}
)

func rawSignatureTag(tag uint32) uint32 {
	if tag == rpmutils.SIG_PGP || tag == rpmutils.SIG_GPG {
		return tag - 16384 // go-rpmutils offsets overlapping signature tags.
	}
	return tag
}

func TestSignaturePacketFuzzSeeds(t *testing.T) {
	for _, tc := range []struct {
		tag    uint32
		packet []byte
		want   string
	}{
		{rpmutils.SIG_RSA, rsaPacket, "RSA"},
		{rpmutils.SIG_PGP, rsaPacket, "PGP"},
		{rpmutils.SIG_DSA, eddsaPacket, "DSA"},
		{rpmutils.SIG_GPG, eddsaPacket, "GPG"},
	} {
		t.Run(fmt.Sprintf("tag=%d", tc.tag), func(t *testing.T) {
			m, err := Bytes(testRPM(nil, &rpmTestTag{tag: rawSignatureTag(tc.tag), kind: rpmutils.RPM_BIN_TYPE, count: uint32(len(tc.packet)), data: tc.packet}))
			if err != nil {
				t.Fatal(err)
			}
			if len(m.SignatureTags) != 1 || m.SignatureTags[0] != tc.want {
				t.Fatalf("signature tags = %v, want %s", m.SignatureTags, tc.want)
			}
		})
	}
}

// The outer RPM headers stay valid so mutations reach packet.Read and the
// algorithm checks in inspect.rpm. The four tag choices cover header and
// full-payload signatures for RSA and EdDSA.
func FuzzRPMSignaturePacket(f *testing.F) {
	for _, tc := range []struct {
		tag    uint8
		packet []byte
	}{
		{0, rsaPacket}, {1, eddsaPacket}, {2, rsaPacket}, {3, eddsaPacket},
		{0, rsaPacket[:len(rsaPacket)-1]},
		{0, append(bytes.Clone(rsaPacket), 0)},
		{1, []byte{0xc2, 1, 4}},
		{2, []byte{0xc0, 1, 4}},
	} {
		f.Add(tc.tag, tc.packet)
	}
	f.Fuzz(func(t *testing.T, choice uint8, packet []byte) {
		if len(packet) == 0 || len(packet) > 64*1024 {
			t.Skip()
		}
		tag := [...]uint32{rpmutils.SIG_RSA, rpmutils.SIG_DSA, rpmutils.SIG_PGP, rpmutils.SIG_GPG}[choice%4]
		_, _ = Bytes(testRPM(nil, &rpmTestTag{tag: rawSignatureTag(tag), kind: rpmutils.RPM_BIN_TYPE, count: uint32(len(packet)), data: packet}))
	})
}

// Rebuild only the gzip member under test. A valid companion member and ar
// envelope ensure gzip mutations reach the DEB reader.
func FuzzDEBGzipMember(f *testing.F) {
	control := testGzip(f, testTar(f, tar.FormatUSTAR, []tarEntry{regularEntry("./control", testControl)}))
	payload := testGzip(f, testTar(f, tar.FormatUSTAR, []tarEntry{regularEntry("./usr/share/doc/hello/README", "hello\n")}))
	for _, tc := range []struct {
		member uint8
		data   []byte
	}{{0, control}, {1, payload}, {0, control[:len(control)-4]}, {1, payload[:len(payload)-4]}, {0, []byte("not gzip")}} {
		f.Add(tc.member, tc.data)
	}
	f.Fuzz(func(t *testing.T, member uint8, compressed []byte) {
		if len(compressed) > 64*1024 {
			t.Skip()
		}
		ctrl, data := control, payload
		if member%2 == 0 {
			ctrl = compressed
		} else {
			data = compressed
		}
		_, _ = Bytes(testDEB(t, ctrl, data))
	})
}

// Compress mutated tar bytes into a valid gzip stream, so tar header, path,
// entry type, and member body handling are exercised directly.
func FuzzDEBTarMember(f *testing.F) {
	control := testTar(f, tar.FormatUSTAR, []tarEntry{regularEntry("./control", testControl), regularEntry("./md5sums", "checksum\n")})
	payload := testTar(f, tar.FormatUSTAR, []tarEntry{regularEntry("./usr/share/doc/hello/README", "hello\n")})
	f.Add(uint8(0), control)
	f.Add(uint8(1), payload)
	f.Add(uint8(0), control[:512])
	f.Add(uint8(1), payload[:512])
	f.Fuzz(func(t *testing.T, member uint8, raw []byte) {
		if len(raw) > 128*1024 {
			t.Skip()
		}
		ctrl, data := control, payload
		if member%2 == 0 {
			ctrl = raw
		} else {
			data = raw
		}
		_, _ = Bytes(testDEB(t, testGzip(t, ctrl), testGzip(t, data)))
	})
}

// Keep tar, gzip, and ar valid while mutating the control file itself.
func FuzzDEBControlFields(f *testing.F) {
	for _, control := range []string{
		testControl,
		testControl + "Depends: libc6,\n\tbase-files\n",
		testControl + "\nPackage: second\n",
		"Package: hello\n",
		"Package: hello\nDescription: \xff\n",
	} {
		f.Add([]byte(control))
	}
	payload := testGzip(f, testTar(f, tar.FormatUSTAR, nil))
	f.Fuzz(func(t *testing.T, control []byte) {
		if len(control) > 16*1024 {
			t.Skip()
		}
		entry := regularEntry("./control", string(control))
		raw := testTar(t, tar.FormatUSTAR, []tarEntry{entry, regularEntry("./md5sums", "")})
		_, _ = Bytes(testDEB(t, testGzip(t, raw), payload))
	})
}

// Use a separate target for raw RPM header lengths and indexes. The existing
// whole-package target keeps real signed and unsigned fixture bytes as seeds.
func FuzzRPMHeaderPacket(f *testing.F) {
	base := testRPM(nil)
	f.Add(base)
	f.Add(testRPM(map[uint32]*rpmTestTag{
		rpmutils.SUMMARY: rpmStrings(rpmutils.SUMMARY, rpmutils.RPM_I18NSTRING_TYPE, "summary", "résumé"),
	}))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 128*1024 {
			t.Skip()
		}
		_, _ = Bytes(raw)
	})
}

// Verify the DEB tar and gzip seeds are accepted before fuzzing. This keeps
// fuzzing focused on parser behavior instead of invalid test envelopes.
func TestDEBPacketFuzzSeeds(t *testing.T) {
	control := testGzip(t, testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("./control", testControl)}))
	payload := testGzip(t, testTar(t, tar.FormatUSTAR, []tarEntry{regularEntry("./payload", "payload")}))
	if _, err := Bytes(testDEB(t, control, payload)); err != nil {
		t.Fatal(err)
	}
}
