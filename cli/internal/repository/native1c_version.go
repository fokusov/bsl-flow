package repository

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
)

// This file ports the 1cv8.exe identity check of Get-BFNativeDependencies:
// the platform FileVersion comes from the PE version resource fixed file
// info, read with the standard library only.

var errNative1CNoVersionResource = errors.New("no version resource")

// native1CPEFileVersion returns the .NET-style four-part FileVersion of a PE
// executable ("major.minor.build.private"), derived from the VS_FIXEDFILEINFO
// of the version resource exactly like VersionInfo.FileVersion does.
func native1CPEFileVersion(data []byte) (string, error) {
	file, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer file.Close()
	section := file.Section(".rsrc")
	if section == nil {
		return "", errNative1CNoVersionResource
	}
	raw, err := section.Data()
	if err != nil {
		return "", err
	}
	// The VS_FIXEDFILEINFO begins with dwSignature 0xFEEF04BD (little-endian
	// bytes BD 04 EF FE), followed by dwFileVersionMS and dwFileVersionLS.
	for index := 0; index+12 <= len(raw); index++ {
		if raw[index] != 0xBD || raw[index+1] != 0x04 || raw[index+2] != 0xEF || raw[index+3] != 0xFE {
			continue
		}
		ms := binary.LittleEndian.Uint32(raw[index+4 : index+8])
		ls := binary.LittleEndian.Uint32(raw[index+8 : index+12])
		return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xffff, ls>>16, ls&0xffff), nil
	}
	return "", errNative1CNoVersionResource
}
