package contract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// checkRPMHeader bounds every index entry before go-rpmutils allocates or
// slices from untrusted header lengths. Unknown tags with valid types remain
// supported. The signature header includes padding to an eight-byte boundary.
func checkRPMHeader(raw []byte, signature bool) (int, error) {
	if len(raw) < 16 || !bytes.Equal(raw[:8], []byte{0x8e, 0xad, 0xe8, 1, 0, 0, 0, 0}) {
		return 0, errors.New("invalid RPM header intro")
	}
	entries := uint64(binary.BigEndian.Uint32(raw[8:12]))
	size := uint64(binary.BigEndian.Uint32(raw[12:16]))
	dataStart := 16 + entries*16
	end := dataStart + size
	paddedEnd := end
	if signature {
		paddedEnd = (end + 7) / 8 * 8
	}
	// The library multiplies index lengths in uint32 and reads offsets and
	// counts as int32. Check those limits as well as the available bytes.
	if entries > math.MaxUint32/16 || size > math.MaxInt32 || paddedEnd > uint64(len(raw)) {
		return 0, errors.New("invalid RPM header length")
	}
	data := raw[dataStart:end]
	for n := uint64(0); n < entries; n++ {
		entry := raw[16+n*16 : 32+n*16]
		tag := binary.BigEndian.Uint32(entry[:4])
		kind := binary.BigEndian.Uint32(entry[4:8])
		offset := uint64(binary.BigEndian.Uint32(entry[8:12]))
		count := uint64(binary.BigEndian.Uint32(entry[12:16]))
		if offset > size || offset > math.MaxInt32 || count == 0 || count > math.MaxInt32 {
			return 0, fmt.Errorf("invalid RPM tag %d bounds", tag)
		}
		var width uint64
		switch kind {
		case 1, 2, 7:
			width = 1 // CHAR, INT8, BIN
		case 3:
			width = 2
		case 4:
			width = 4
		case 5:
			width = 8
		case 6, 8, 9: // STRING, STRING_ARRAY, I18NSTRING
			if kind == 6 && count != 1 {
				return 0, fmt.Errorf("invalid RPM string tag %d count", tag)
			}
			remaining := data[offset:]
			if count > uint64(len(remaining)) {
				return 0, fmt.Errorf("truncated RPM string tag %d", tag)
			}
			for i := uint64(0); i < count; i++ {
				end := bytes.IndexByte(remaining, 0)
				if end < 0 {
					return 0, fmt.Errorf("unterminated RPM string tag %d", tag)
				}
				remaining = remaining[end+1:]
			}
			continue
		default:
			return 0, fmt.Errorf("invalid RPM tag %d type", tag)
		}
		if offset%width != 0 || count > (size-offset)/width {
			return 0, fmt.Errorf("invalid RPM tag %d data range", tag)
		}
	}
	return int(paddedEnd), nil
}
