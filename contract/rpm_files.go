package contract

import (
	"errors"
	"fmt"

	rpmutils "github.com/sassoftware/go-rpmutils"
)

// checkRPMFiles validates the parallel file arrays used by GetFiles. The
// library indexes those arrays without checking their lengths or directory IDs.
func checkRPMFiles(hdr *rpmutils.RpmHeader) error {
	var names []string
	var err error
	if hdr.HasTag(rpmutils.OLDFILENAMES) {
		names, err = hdr.GetStrings(rpmutils.OLDFILENAMES)
	} else if hdr.HasTag(rpmutils.BASENAMES) {
		names, err = hdr.GetStrings(rpmutils.BASENAMES)
		dirs, dirsErr := hdr.GetStrings(rpmutils.DIRNAMES)
		indexes, indexesErr := hdr.GetUint32s(rpmutils.DIRINDEXES)
		if err != nil || dirsErr != nil || indexesErr != nil || len(names) != len(indexes) {
			return errors.New("invalid RPM file directory fields")
		}
		for _, index := range indexes {
			if uint64(index) >= uint64(len(dirs)) {
				return errors.New("invalid RPM file directory index")
			}
		}
	} else {
		if hdr.HasTag(rpmutils.DIRNAMES) || hdr.HasTag(rpmutils.DIRINDEXES) {
			return errors.New("missing RPM file names")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("invalid RPM file names: %w", err)
	}
	for _, tag := range []int{rpmutils.FILEUSERNAME, rpmutils.FILEGROUPNAME, rpmutils.FILEDIGESTS, rpmutils.FILELINKTOS} {
		values, err := hdr.GetStrings(tag)
		if err != nil || len(values) != len(names) {
			return fmt.Errorf("invalid RPM file tag %d", tag)
		}
	}
	for _, tag := range []int{rpmutils.FILEFLAGS, rpmutils.FILEMTIMES, rpmutils.FILEMODES, rpmutils.FILEDEVICES, rpmutils.FILEINODES} {
		if (tag == rpmutils.FILEDEVICES || tag == rpmutils.FILEINODES) && !hdr.HasTag(tag) {
			continue
		}
		values, err := hdr.GetUint32s(tag)
		if err != nil || len(values) != len(names) {
			return fmt.Errorf("invalid RPM file tag %d", tag)
		}
	}
	sizeTag := rpmutils.FILESIZES
	if hdr.HasTag(rpmutils.LONGFILESIZES) {
		sizeTag = rpmutils.LONGFILESIZES
	}
	sizes, err := hdr.GetUint64s(sizeTag)
	if err != nil || len(sizes) != len(names) {
		return errors.New("invalid RPM file sizes")
	}
	return nil
}
