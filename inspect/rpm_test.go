package inspect

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	rpmutils "github.com/sassoftware/go-rpmutils"
)

type rpmTestTag struct {
	tag, kind, count uint32
	data             []byte
}

func rpmStrings(tag, kind uint32, values ...string) *rpmTestTag {
	return &rpmTestTag{tag, kind, uint32(len(values)), []byte(strings.Join(values, "\x00") + "\x00")}
}

func rpmInts(tag uint32, values ...uint32) *rpmTestTag {
	entry := &rpmTestTag{tag: tag, kind: rpmutils.RPM_INT32_TYPE, count: uint32(len(values))}
	for _, value := range values {
		entry.data = binary.BigEndian.AppendUint32(entry.data, value)
	}
	return entry
}

// Small independent header encoder. Synthetic packages have no digests so
// malformed tag tests reach validation rather than fail a digest check first.
func testRPMHeader(entries []*rpmTestTag, signature bool) []byte {
	entries = append([]*rpmTestTag(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].tag < entries[j].tag })
	var index, data []byte
	for _, entry := range entries {
		align := 1
		switch entry.kind {
		case rpmutils.RPM_INT16_TYPE:
			align = 2
		case rpmutils.RPM_INT32_TYPE:
			align = 4
		case rpmutils.RPM_INT64_TYPE:
			align = 8
		}
		for len(data)%align != 0 {
			data = append(data, 0)
		}
		for _, value := range []uint32{entry.tag, entry.kind, uint32(len(data)), entry.count} {
			index = binary.BigEndian.AppendUint32(index, value)
		}
		data = append(data, entry.data...)
	}
	out := []byte{0x8e, 0xad, 0xe8, 1, 0, 0, 0, 0}
	out = binary.BigEndian.AppendUint32(out, uint32(len(entries)))
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, index...)
	out = append(out, data...)
	if signature {
		for len(out)%8 != 0 {
			out = append(out, 0)
		}
	}
	return out
}

func testRPM(changes map[uint32]*rpmTestTag, signature ...*rpmTestTag) []byte {
	tags := map[uint32]*rpmTestTag{}
	for tag, value := range map[uint32]string{
		rpmutils.NAME: "fixture", rpmutils.VERSION: "1.2", rpmutils.RELEASE: "3",
		rpmutils.ARCH: "noarch", rpmutils.PAYLOADCOMPRESSOR: "gzip",
		rpmutils.PAYLOADFORMAT: "cpio", rpmutils.LICENSE: "GPL-3.0-only",
	} {
		tags[tag] = rpmStrings(tag, rpmutils.RPM_STRING_TYPE, value)
	}
	for tag, entry := range changes {
		if entry == nil {
			delete(tags, tag)
		} else {
			tags[tag] = entry
		}
	}
	entries := make([]*rpmTestTag, 0, len(tags))
	for _, entry := range tags {
		entries = append(entries, entry)
	}
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb, 3})
	binary.BigEndian.PutUint16(lead[78:80], 5)
	return append(append(lead, testRPMHeader(signature, true)...), testRPMHeader(entries, false)...)
}

func TestRPMMetadataCompatibility(t *testing.T) {
	for _, epoch := range []uint32{0, 2, 4294967295} {
		for _, arch := range []string{"noarch", "x86_64", "aarch64"} {
			t.Run(arch+"/epoch="+fmt.Sprint(epoch), func(t *testing.T) {
				m, err := Bytes(testRPM(map[uint32]*rpmTestTag{
					rpmutils.EPOCH:          rpmInts(rpmutils.EPOCH, epoch),
					rpmutils.ARCH:           rpmStrings(rpmutils.ARCH, rpmutils.RPM_STRING_TYPE, arch),
					100:                     rpmStrings(100, rpmutils.RPM_STRING_ARRAY_TYPE, "C", "fr"),
					rpmutils.SUMMARY:        rpmStrings(rpmutils.SUMMARY, rpmutils.RPM_I18NSTRING_TYPE, "Default summary", "Résumé"),
					rpmutils.DESCRIPTION:    rpmStrings(rpmutils.DESCRIPTION, rpmutils.RPM_I18NSTRING_TYPE, "Default description", "Description française"),
					60000:                   rpmStrings(60000, rpmutils.RPM_STRING_TYPE, "unknown extension"),
					rpmutils.REQUIRENAME:    rpmStrings(rpmutils.REQUIRENAME, rpmutils.RPM_STRING_ARRAY_TYPE, "rpmlib(CompressedFileNames)", "(foo if bar)"),
					rpmutils.REQUIREVERSION: rpmStrings(rpmutils.REQUIREVERSION, rpmutils.RPM_STRING_ARRAY_TYPE, "3.0.4-1", ""),
					rpmutils.REQUIREFLAGS:   rpmInts(rpmutils.REQUIREFLAGS, 16777226, 0),
				}))
				if err != nil {
					t.Fatal(err)
				}
				if m.Identity.Epoch != fmt.Sprint(epoch) || m.Identity.Architecture != arch || m.Fields["Summary"] != "Default summary" || m.Fields["Description"] != "Default description" {
					t.Fatalf("metadata = %+v", m)
				}
				if len(m.Requirements) != 2 || m.Requirements[1].Name != "(foo if bar)" || m.Requirements[0].Version != "3.0.4-1" {
					t.Fatalf("dependencies = %+v", m.Requirements)
				}
			})
		}
	}
}

func TestRPMRejectsMalformedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[uint32]*rpmTestTag
	}{
		{"orphan versions", map[uint32]*rpmTestTag{rpmutils.REQUIREVERSION: rpmStrings(rpmutils.REQUIREVERSION, rpmutils.RPM_STRING_ARRAY_TYPE, "1")}},
		{"orphan flags", map[uint32]*rpmTestTag{rpmutils.REQUIREFLAGS: rpmInts(rpmutils.REQUIREFLAGS, 0)}},
		{"wrong dependency type", map[uint32]*rpmTestTag{rpmutils.REQUIRENAME: rpmInts(rpmutils.REQUIRENAME, 0)}},
		{"missing dependency companions", map[uint32]*rpmTestTag{rpmutils.REQUIRENAME: rpmStrings(rpmutils.REQUIRENAME, rpmutils.RPM_STRING_ARRAY_TYPE, "foo")}},
		{"wrong summary type", map[uint32]*rpmTestTag{rpmutils.SUMMARY: rpmInts(rpmutils.SUMMARY, 1)}},
		{"wrong license type", map[uint32]*rpmTestTag{rpmutils.LICENSE: rpmInts(rpmutils.LICENSE, 1)}},
		{"multiple names", map[uint32]*rpmTestTag{rpmutils.NAME: rpmStrings(rpmutils.NAME, rpmutils.RPM_STRING_ARRAY_TYPE, "one", "two")}},
		{"multiple epochs", map[uint32]*rpmTestTag{rpmutils.EPOCH: rpmInts(rpmutils.EPOCH, 1, 2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Bytes(testRPM(tc.changes)); err == nil {
				t.Fatal("accepted malformed metadata")
			}
		})
	}
	for _, tag := range []uint32{268, 267, 1002, 1005} {
		t.Run("signature="+fmt.Sprint(tag), func(t *testing.T) {
			if _, err := Bytes(testRPM(nil, rpmStrings(tag, rpmutils.RPM_STRING_TYPE, "not a signature"))); err == nil {
				t.Fatal("ignored malformed signature tag")
			}
		})
	}
}

func TestRPMHeaderBounds(t *testing.T) {
	for _, signature := range []bool{false, true} {
		base := testRPM(nil)
		start := 112 // Empty signature header occupies 16 bytes.
		if signature {
			base = testRPM(nil, rpmStrings(60000, rpmutils.RPM_STRING_TYPE, "extension"))
			start = 96
		}
		for _, tc := range []struct {
			name   string
			offset int
			value  uint32
		}{
			{"huge index", 8, 0xffffffff},
			{"huge data", 12, 0xffffffff},
			{"negative offset", 24, 0xffffffff},
			{"past end offset", 24, 0x7fffffff},
			{"zero count", 28, 0},
			{"negative count", 28, 0xffffffff},
			{"unknown type", 20, 255},
		} {
			t.Run(fmt.Sprintf("signature=%v/%s", signature, tc.name), func(t *testing.T) {
				raw := bytes.Clone(base)
				binary.BigEndian.PutUint32(raw[start+tc.offset:], tc.value)
				if _, err := Bytes(raw); err == nil {
					t.Fatal("accepted malformed RPM header")
				}
			})
		}
	}
}

func FuzzRPMInspection(f *testing.F) {
	f.Add(testRPM(nil))
	for _, name := range []string{"empty-0.1-1.x86_64.rpm", "payload-gzip.rpm", "payload-xz.rpm"} {
		raw, err := os.ReadFile(filepath.Join("../contract/testdata/packages", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1024*1024 {
			t.Skip()
		}
		_, _ = rpm(raw)
	})
}

func TestRPMFileArrayValidation(t *testing.T) {
	valid := func() map[uint32]*rpmTestTag {
		tags := map[uint32]*rpmTestTag{
			rpmutils.BASENAMES:  rpmStrings(rpmutils.BASENAMES, rpmutils.RPM_STRING_ARRAY_TYPE, "notes..txt", "other"),
			rpmutils.DIRNAMES:   rpmStrings(rpmutils.DIRNAMES, rpmutils.RPM_STRING_ARRAY_TYPE, "/usr/share/"),
			rpmutils.DIRINDEXES: rpmInts(rpmutils.DIRINDEXES, 0, 0),
		}
		for _, tag := range []uint32{rpmutils.FILEUSERNAME, rpmutils.FILEGROUPNAME, rpmutils.FILEDIGESTS, rpmutils.FILELINKTOS} {
			tags[tag] = rpmStrings(tag, rpmutils.RPM_STRING_ARRAY_TYPE, "", "")
		}
		for _, tag := range []uint32{rpmutils.FILESIZES, rpmutils.FILEFLAGS, rpmutils.FILEMTIMES, rpmutils.FILEMODES} {
			tags[tag] = rpmInts(tag, 0, 0)
		}
		return tags
	}
	if _, err := Bytes(testRPM(valid())); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tag   uint32
		entry *rpmTestTag
	}{
		{"bad directory index", rpmutils.DIRINDEXES, rpmInts(rpmutils.DIRINDEXES, 0, 1)},
		{"short directory indexes", rpmutils.DIRINDEXES, rpmInts(rpmutils.DIRINDEXES, 0)},
		{"missing directories", rpmutils.DIRNAMES, nil},
		{"missing names", rpmutils.BASENAMES, nil},
		{"short sizes", rpmutils.FILESIZES, rpmInts(rpmutils.FILESIZES, 0)},
		{"short modes", rpmutils.FILEMODES, rpmInts(rpmutils.FILEMODES, 0)},
		{"short links", rpmutils.FILELINKTOS, rpmStrings(rpmutils.FILELINKTOS, rpmutils.RPM_STRING_ARRAY_TYPE, "")},
		{"short devices", rpmutils.FILEDEVICES, rpmInts(rpmutils.FILEDEVICES, 0)},
		{"short inodes", rpmutils.FILEINODES, rpmInts(rpmutils.FILEINODES, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := valid()
			if tc.entry == nil {
				delete(tags, tc.tag)
			} else {
				tags[tc.tag] = tc.entry
			}
			if _, err := Bytes(testRPM(tags)); err == nil {
				t.Fatal("accepted malformed file arrays")
			}
		})
	}
}
