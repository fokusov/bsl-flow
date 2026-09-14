package memoryhost

// This file ports the path contract helpers the bridge and the memory plane
// depend on: Assert-BFSafePath and Assert-BFUuid (Task.Storage.ps1 /
// Task.Contracts.ps1), Assert-BFRelativePath (Task.Contracts.ps1) and the
// small string primitives whose UTF-16 lengths and whitespace classes must
// match the PowerShell originals.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	uuidPattern         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hashPattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pathProviderPattern = regexp.MustCompile(`^[^:]+::`)
	relativeForbidden   = regexp.MustCompile(`[:*?"<>|\x00-\x1f]`)
	relativeDotDot      = regexp.MustCompile(`(^|[\\/])\.\.([\\/]|$)`)
)

// assertUUID ports Assert-BFUuid.
func assertUUID(value string) error {
	if !uuidPattern.MatchString(value) {
		return newBFError("BF_INVALID", "identity must be a canonical lower-case UUID.")
	}
	return nil
}

// assertRelativePath ports Assert-BFRelativePath.
func assertRelativePath(value string) error {
	if isNullOrWhiteSpace(value) || filepath.IsAbs(value) ||
		relativeForbidden.MatchString(value) || relativeDotDot.MatchString(value) {
		return newBFError("BF_INVALID", "unsafe relative path: "+value)
	}
	return nil
}

// assertSafePath ports Assert-BFSafePath: an ordinary absolute filesystem
// path without device/provider qualification, alternate data streams or
// reparse points anywhere along the existing ancestor chain. It returns the
// cleaned full path.
func assertSafePath(path string) (string, error) {
	if isNullOrWhiteSpace(path) || !filepath.IsAbs(path) {
		return "", bfInvalid("Path must be an absolute filesystem path.")
	}
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.`) || pathProviderPattern.MatchString(path) {
		return "", bfInvalid("Device and provider-qualified paths are not allowed.")
	}
	fullPath := filepath.Clean(path)
	if !filepath.IsAbs(fullPath) {
		absolute, err := filepath.Abs(fullPath)
		if err != nil {
			return "", bfInvalid("Path is invalid: %s", err.Error())
		}
		fullPath = absolute
	}
	root := pathRoot(fullPath)
	if strings.Contains(fullPath[len(root):], ":") {
		return "", bfInvalid("Alternate data stream paths are not allowed.")
	}
	cursor := fullPath
	for cursor != "" {
		if info, err := os.Lstat(cursor); err == nil {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return "", bfInvalid("Path contains a reparse point: %s", cursor)
			}
		}
		trimmed := trimPathSeparators(cursor)
		if trimmed == "" {
			break
		}
		parent := filepath.Dir(trimmed)
		if parent == "" || parent == cursor || parent == trimmed {
			break
		}
		cursor = parent
	}
	return fullPath, nil
}

// pathRoot mirrors [System.IO.Path]::GetPathRoot for the two shapes the
// helper accepts (Windows volumes and POSIX roots).
func pathRoot(path string) string {
	volume := filepath.VolumeName(path)
	if volume != "" {
		if strings.HasSuffix(volume, `\`) || strings.HasSuffix(volume, "/") {
			return volume
		}
		return volume + `\`
	}
	if strings.HasPrefix(path, "/") {
		return "/"
	}
	return ""
}

// trimPathSeparators trims trailing separator characters the way the
// PowerShell TrimEnd(DirectorySeparatorChar, AltDirectorySeparatorChar)
// calls do: both separators on Windows, only '/' elsewhere.
func trimPathSeparators(path string) string {
	if runtime.GOOS == "windows" {
		return strings.TrimRight(path, `\/`)
	}
	return strings.TrimRight(path, `/`)
}

// isNullOrWhiteSpace mirrors [string]::IsNullOrWhiteSpace over Unicode
// whitespace.
func isNullOrWhiteSpace(value string) bool {
	if value == "" {
		return true
	}
	for _, character := range value {
		if !unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

// utf16Length mirrors the PowerShell string .Length counter (UTF-16 code
// units), which bounds every text field in the bridge contract.
func utf16Length(value string) int {
	count := 0
	for _, character := range value {
		if character > 0xFFFF {
			count += 2
		} else {
			count++
		}
	}
	return count
}

// substringUTF16 cuts the string to at most limit UTF-16 code units without
// splitting a surrogate pair (PowerShell would split it and produce an
// unpaired surrogate; Go cannot represent that, so it cuts at the rune
// boundary at or before the limit).
func substringUTF16(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index, character := range value {
		units := 1
		if character > 0xFFFF {
			units = 2
		}
		if count+units > limit {
			return value[:index]
		}
		count += units
	}
	return value
}

// trimEndUnicode mirrors the parameterless .NET TrimEnd().
func trimEndUnicode(value string) string {
	return strings.TrimRightFunc(value, unicode.IsSpace)
}

// strictUTF8Decode mirrors [Text.UTF8Encoding]::new($false, $true).GetString:
// invalid byte sequences are rejected instead of being replaced.
func strictUTF8Decode(data []byte) (string, bool) {
	if !utf8.Valid(data) {
		return "", false
	}
	if !utf8.ValidString(string(data)) {
		return "", false
	}
	return string(data), true
}

// isHexHash reports the lower-case SHA-256 shape used across the contract.
func isHexHash(value string) bool { return hashPattern.MatchString(value) }
