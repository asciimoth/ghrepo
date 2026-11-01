package contract

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	rpmutils "github.com/sassoftware/go-rpmutils"
)

var (
	ErrMalformedDEB              = errors.New("malformed DEB archive")
	ErrUnsupportedDEBCompression = errors.New("unsupported DEB control compression")
	ErrUnsupportedRPMVersion     = errors.New("unsupported RPM package version")
	ErrUnsupportedRPMCompression = errors.New("unsupported RPM payload compression")
)

// CheckDEBFormat checks the supported archive envelope. It does not parse the
// control fields or package payload; the package inspector does that later.
func CheckDEBFormat(data []byte) error {
	if len(data) < 8 || string(data[:8]) != "!<arch>\n" {
		return ErrMalformedDEB
	}
	data = data[8:]
	member := 0
	for len(data) > 0 {
		if len(data) < 60 || string(data[58:60]) != "`\n" {
			return ErrMalformedDEB
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(data[:16])), "/")
		size, err := strconv.Atoi(strings.TrimSpace(string(data[48:58])))
		if err != nil || size < 0 || size > len(data)-60 {
			return ErrMalformedDEB
		}
		body := data[60 : 60+size]
		switch member {
		case 0:
			if name != "debian-binary" || string(body) != "2.0\n" {
				return fmt.Errorf("%w: invalid debian-binary member", ErrMalformedDEB)
			}
		case 1:
			if name != "control.tar.gz" {
				return fmt.Errorf("%w: %s", ErrUnsupportedDEBCompression, name)
			}
		case 2:
			if name != "data.tar.gz" {
				return fmt.Errorf("%w: %s", ErrUnsupportedDEBCompression, name)
			}
		default:
			return fmt.Errorf("%w: extra member %s", ErrMalformedDEB, name)
		}
		member++
		data = data[60+size:]
		if size%2 != 0 {
			if len(data) == 0 {
				return ErrMalformedDEB
			}
			data = data[1:]
		}
	}
	if member != 3 {
		return fmt.Errorf("%w: missing archive member", ErrMalformedDEB)
	}
	return nil
}

// CheckRPMFormat checks the supported RPM lead and payload compression. It
// leaves package identity and signature validation to the package inspector.
func CheckRPMFormat(data []byte) error {
	if len(data) < 96 || !bytes.Equal(data[:4], []byte{0xed, 0xab, 0xee, 0xdb}) || data[4] != 3 {
		return ErrUnsupportedRPMVersion
	}
	n, err := checkRPMHeader(data[96:], true)
	if err != nil {
		return fmt.Errorf("malformed RPM signature header: %w", err)
	}
	if _, err := checkRPMHeader(data[96+n:], false); err != nil {
		return fmt.Errorf("malformed RPM header: %w", err)
	}
	hdr, err := rpmutils.ReadHeader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("malformed RPM header: %w", err)
	}
	compression, err := hdr.GetString(rpmutils.PAYLOADCOMPRESSOR)
	if err != nil {
		return fmt.Errorf("%w: missing compressor", ErrUnsupportedRPMCompression)
	}
	if compression != "gzip" && compression != "xz" && compression != "zstd" {
		return fmt.Errorf("%w: %s", ErrUnsupportedRPMCompression, compression)
	}
	return checkRPMFiles(hdr)
}
