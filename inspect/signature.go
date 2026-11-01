package inspect

import "encoding/binary"

// completeSignaturePacket checks framing before packet.Read. That parser can
// return success as soon as the signature fields are read, without checking
// that the packet's declared body length was fully present.
func completeSignaturePacket(raw []byte) bool {
	if len(raw) < 2 {
		return false
	}
	var size uint64
	var start int
	if raw[0] == 0xc2 { // New-format OpenPGP signature packet.
		switch {
		case raw[1] < 192:
			start, size = 2, uint64(raw[1])
		case raw[1] < 224:
			if len(raw) < 3 {
				return false
			}
			start, size = 3, (uint64(raw[1])-192)*256+uint64(raw[2])+192
		case raw[1] == 255:
			if len(raw) < 6 {
				return false
			}
			start, size = 6, uint64(binary.BigEndian.Uint32(raw[2:6]))
		default:
			// Partial body lengths are for data packets, not signatures.
			return false
		}
	} else if raw[0]&0xfc == 0x88 { // Old-format signature packet.
		switch raw[0] & 3 {
		case 0:
			start, size = 2, uint64(raw[1])
		case 1:
			if len(raw) < 3 {
				return false
			}
			start, size = 3, uint64(binary.BigEndian.Uint16(raw[1:3]))
		case 2:
			if len(raw) < 5 {
				return false
			}
			start, size = 5, uint64(binary.BigEndian.Uint32(raw[1:5]))
		case 3:
			// Legacy indeterminate length extends to the end of the tag.
			return true
		}
	} else {
		return false
	}
	return size == uint64(len(raw)-start)
}
