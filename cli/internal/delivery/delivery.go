// Package delivery implements the native publication and delivery-handoff
// contracts of the controller: plans are built only from an accepted
// revision, publication is a crash-resumable state machine driven through an
// injected Git port, and a push whose effect cannot be observed blocks
// automatic replay until the remote is reconciled.
package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Blocker classes carried by Error, matching the legacy controller envelopes.
const (
	KindInvalid  = "BF_INVALID"
	KindBlocked  = "BF_BLOCKED"
	KindConflict = "BF_CONFLICT"
)

// Error is a classified controller blocker; Kind selects the exit mapping.
type Error struct {
	Kind    string
	Message string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Message }

func invalid(format string, args ...any) error {
	return &Error{Kind: KindInvalid, Message: fmt.Sprintf(format, args...)}
}

func blocked(format string, args ...any) error {
	return &Error{Kind: KindBlocked, Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &Error{Kind: KindConflict, Message: fmt.Sprintf(format, args...)}
}

// UnknownEffectError reports an operation whose side effect could not be
// observed. The publication outcome stays unknown; it is never retried or
// re-dispatched automatically.
type UnknownEffectError struct {
	Operation string
	Cause     error
}

func (e *UnknownEffectError) Error() string {
	if e.Cause == nil {
		return "unknown effect after " + e.Operation
	}
	return "unknown effect after " + e.Operation + ": " + e.Cause.Error()
}

func (e *UnknownEffectError) Unwrap() error { return e.Cause }

// CommitID is a Git object identity in either the SHA-1 or SHA-256 object format.
type CommitID string

// GitPort is the only Git surface the publication state machine may drive;
// this package never executes Git itself.
//
// ReadRemoteHead returns the definitive head of remote/ref: known=true with a
// nil error is an exact answer (an empty CommitID means the ref is
// definitively absent) while known=false reports an undeterminable head whose
// typed cause is carried in err.
type GitPort interface {
	Stage(path string) error
	CommitAll(message string) (CommitID, error)
	Push(remote, ref string) error
	ReadRemoteHead(remote, ref string) (CommitID, bool, error)
}

// Target binds the exact publication destination with its authorization
// profile and the closed set of relative paths the target admits.
type Target struct {
	Remote       string
	Ref          string
	AuthorizedBy string
	AllowedPaths []string
}

var (
	publicationRefPattern = regexp.MustCompile(`^refs/heads/codex/[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	githubRemotePattern   = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9]([A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?)/([A-Za-z0-9]([A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?)\.git$`)
)

// Validate enforces the exact-target and authorization binding: a narrow
// refs/heads/codex/* ref, a remote that is either an absolute local path
// published without credentials or exactly https://github.com/OWNER/REPO.git
// published through the GitHub CLI profile, and a closed, duplicate-free set
// of admitted relative paths.
func (t Target) Validate() error {
	if err := validateRemote(t.Remote, t.AuthorizedBy); err != nil {
		return err
	}
	if err := validateRef(t.Ref); err != nil {
		return err
	}
	return validateScopes(t.AllowedPaths)
}

func validateRemote(remote, authorizedBy string) error {
	if strings.TrimSpace(remote) == "" || strings.ContainsAny(remote, "\x00\r\n") || strings.HasPrefix(remote, "-") {
		return invalid("publication remote is missing or malformed")
	}
	if strings.Contains(remote, "://") {
		match := githubRemotePattern.FindStringSubmatch(remote)
		if match == nil || match[1] == "." || match[1] == ".." || match[3] == "." || match[3] == ".." {
			return invalid("HTTPS remote must be exactly https://github.com/OWNER/REPO.git")
		}
		if authorizedBy != "github_cli" {
			return invalid("GitHub HTTPS publication requires auth=github_cli")
		}
		return nil
	}
	if !filepath.IsAbs(remote) {
		return invalid("publication remote must be an absolute local path or exactly https://github.com/OWNER/REPO.git")
	}
	if authorizedBy != "none" {
		return invalid("local FILE publication requires auth=none")
	}
	return nil
}

func validateRef(ref string) error {
	if !publicationRefPattern.MatchString(ref) ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") ||
		strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, "/") {
		return invalid("publication ref must be a narrow refs/heads/codex/* ref")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "refs/heads/"), "/") {
		if segment == "." || strings.HasSuffix(segment, ".lock") {
			return invalid("publication ref must be a narrow refs/heads/codex/* ref")
		}
	}
	return nil
}

// validateScopes keeps AllowedPaths a closed set: every entry is a safe
// relative path (or the whole-tree "." scope) and duplicates, including
// case-collisions, are rejected.
func validateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return invalid("publication target admits no paths")
	}
	ordinal := make(map[string]bool, len(scopes))
	folded := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if scope != "." && !validRelativePath(scope) {
			return invalid("admitted target path must be a non-empty slash-separated relative path: %s", scope)
		}
		key := strings.ToLower(scope)
		if ordinal[scope] || folded[key] {
			return invalid("duplicate or case-colliding admitted target path: %s", scope)
		}
		ordinal[scope] = true
		folded[key] = true
	}
	return nil
}

// validRelativePath accepts only slash-separated relative paths of safe
// segments: traversal (..), current-directory segments, backslashes, NUL and
// control characters, rooted or drive-qualified paths and .git segments are
// rejected.
func validRelativePath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\x00\r\n\t") || strings.Contains(path, `\`) || strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, ".git") || strings.Contains(segment, ":") {
			return false
		}
	}
	return true
}

// pathWithin reports whether path falls inside one of the closed admitted
// scopes; "." admits the whole protected tree.
func pathWithin(path string, scopes []string) bool {
	for _, scope := range scopes {
		if scope == "." || path == scope || strings.HasPrefix(path, scope+"/") {
			return true
		}
	}
	return false
}

// canonicalJSON encodes value with sorted object keys, compact separators and
// raw UTF-8: no HTML escaping and no CR bytes (the only line breaks in string
// values escape as LF), so equal values always produce identical bytes that
// can be hashed or persisted for crash recovery.
func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeCanonical(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeCanonical(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		if !utf8.ValidString(typed) {
			return errors.New("canonical strings must contain valid UTF-8")
		}
		writeCanonicalString(buffer, typed)
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case []string:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if !utf8.ValidString(item) {
				return errors.New("canonical strings must contain valid UTF-8")
			}
			writeCanonicalString(buffer, item)
		}
		buffer.WriteByte(']')
	case []any:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonical(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case map[string]any:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeCanonicalString(buffer, key)
			buffer.WriteByte(':')
			if err := writeCanonical(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical value type %T", value)
	}
	return nil
}

// writeCanonicalString escapes only the characters JSON requires; bytes above
// 0x7F pass through as raw UTF-8 and <, >, & are never HTML-escaped.
func writeCanonicalString(buffer *bytes.Buffer, text string) {
	buffer.WriteByte('"')
	for index := 0; index < len(text); index++ {
		character := text[index]
		switch {
		case character == '"':
			buffer.WriteString(`\"`)
		case character == '\\':
			buffer.WriteString(`\\`)
		case character == '\b':
			buffer.WriteString(`\b`)
		case character == '\t':
			buffer.WriteString(`\t`)
		case character == '\n':
			buffer.WriteString(`\n`)
		case character == '\f':
			buffer.WriteString(`\f`)
		case character == '\r':
			buffer.WriteString(`\r`)
		case character < 0x20:
			fmt.Fprintf(buffer, `\u%04x`, character)
		default:
			buffer.WriteByte(character)
		}
	}
	buffer.WriteByte('"')
}

// sha256Hex returns the lowercase SHA-256 of bytes.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashValue returns the lowercase SHA-256 of the canonical JSON encoding.
func hashValue(value any) (string, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func validCommitID(id CommitID) bool {
	return validHexLength(string(id), 40) || validHexLength(string(id), 64)
}

func validSHA256(text string) bool {
	return validHexLength(text, 64)
}

func validHexLength(text string, length int) bool {
	if len(text) != length {
		return false
	}
	for _, character := range text {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validUUID(text string) bool {
	if len(text) != 36 {
		return false
	}
	for index, character := range text {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", character) {
				return false
			}
		}
	}
	return true
}
