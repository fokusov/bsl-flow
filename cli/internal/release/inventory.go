package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// reservedName mirrors the Windows reserved-name rule of the runtime bundle
// reader in cli/bundle.go.
var reservedName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)`)

// MissingInventoryEntryError reports an inventory file that the resolver
// cannot produce; the runtime mirror is a bundle file absent from the
// extracted cache.
type MissingInventoryEntryError struct {
	Path string
}

func (e *MissingInventoryEntryError) Error() string {
	return fmt.Sprintf("embedded inventory file missing: %q", e.Path)
}

// TamperedInventoryEntryError reports an inventory file whose content hash
// differs from the recorded SHA-256.
type TamperedInventoryEntryError struct {
	Path string
	Want string
	Got  string
}

func (e *TamperedInventoryEntryError) Error() string {
	return fmt.Sprintf("tampered embedded inventory file %q: want sha256 %s, got %s", e.Path, e.Want, e.Got)
}

// ExtraInventoryEntryError reports a forbidden file that resolves anyway.
// The runtime mirror is an unexpected file in the extracted bundle cache.
type ExtraInventoryEntryError struct {
	Path string
}

func (e *ExtraInventoryEntryError) Error() string {
	return fmt.Sprintf("unexpected embedded inventory file: %q", e.Path)
}

// InvalidInventoryEntryError reports an inventory path that fails the same
// path validation the runtime bundle reader enforces, including
// case-insensitive duplicate paths.
type InvalidInventoryEntryError struct {
	Path string
}

func (e *InvalidInventoryEntryError) Error() string {
	return fmt.Sprintf("invalid embedded inventory path %q", e.Path)
}

// VerifyEmbeddedInventory mirrors the runtime embedded-bundle check at
// packaging time. inventory maps relative paths to expected lowercase
// SHA-256 digests; resolve returns the bytes for one relative path and an
// error when the path is absent. Every entry must resolve with a matching
// hash and every path must be a valid forward-slash relative path without
// case-insensitive duplicates. An empty expected digest marks a forbidden
// path: the resolver must fail for it, which detects extra files that the
// release must not embed.
func VerifyEmbeddedInventory(inventory map[string]string, resolve func(rel string) ([]byte, error)) error {
	if resolve == nil {
		return errors.New("inventory resolver is required")
	}
	keys := make([]string, 0, len(inventory))
	for key := range inventory {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, rel := range keys {
		if !validInventoryPath(rel) {
			return &InvalidInventoryEntryError{Path: rel}
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return &InvalidInventoryEntryError{Path: rel}
		}
		seen[key] = true
		content, err := resolve(rel)
		want := strings.ToLower(inventory[rel])
		if err != nil {
			if want == "" {
				continue
			}
			return &MissingInventoryEntryError{Path: rel}
		}
		if want == "" {
			return &ExtraInventoryEntryError{Path: rel}
		}
		sum := sha256.Sum256(content)
		got := hex.EncodeToString(sum[:])
		if got != want {
			return &TamperedInventoryEntryError{Path: rel, Want: want, Got: got}
		}
	}
	return nil
}

// validInventoryPath applies the runtime bundle entry rules: clean
// forward-slash relative paths without drive/backslash separators, control
// characters, dot/space-tail segments, traversal or Windows reserved names.
func validInventoryPath(name string) bool {
	if name == "" || len(name) > 240 || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:<>"|?*`+"\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || reservedName.MatchString(part) {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
	}
	return true
}
