// Generate a deterministic RPM signature parsing fixture. Run from the repo root:
// go run ./contract/testdata/generate-signature
package main

import (
	"encoding/binary"
	"os"

	rpmutils "github.com/sassoftware/go-rpmutils"
)

func main() {
	source, err := os.Open("contract/testdata/packages/empty-0.1-1.x86_64.rpm")
	must(err)
	// Parseable EdDSA/SHA-512 packet with placeholder MPIs, not a valid signature.
	sig := []byte{0xc2, 22, 4, 0, 22, 10, 0, 6, 5, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 1, 0, 1, 1}
	name := "contract/testdata/packages/eddsa-sha512.rpm"
	_, err = rpmutils.RewriteWithSignatures(source, name, sig, sig)
	must(err)
	must(source.Close())
	raw, err := os.ReadFile(name)
	must(err)
	count := binary.BigEndian.Uint32(raw[104:108])
	for i := uint32(0); i < count; i++ {
		entry := raw[112+i*16:]
		switch binary.BigEndian.Uint32(entry) {
		case 268:
			binary.BigEndian.PutUint32(entry, 267)
		case 1002:
			binary.BigEndian.PutUint32(entry, 1005)
		}
	}
	must(os.WriteFile(name, raw, 0644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
