package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is a self-contained port of the canonical JSON writer of the
// PowerShell storage layer (Task.Storage.ps1 ConvertTo-BFJsonString and
// Get-BFCanonicalJson), narrowed to the value shapes this package persists:
// strings, integers, arrays and key-sorted objects. It intentionally
// duplicates cli/internal/worker/canonical.go and the shared algorithm in
// cli/internal/repository: the runner package must stay free of sibling
// imports while those packages develop in parallel. Keep the ports in sync
// when the canonical form changes.

func appendCanonicalString(dst []byte, value string) []byte {
	dst = append(dst, '"')
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character == '"':
			dst = append(dst, '\\', '"')
		case character == '\\':
			dst = append(dst, '\\', '\\')
		case character == '\b':
			dst = append(dst, '\\', 'b')
		case character == '\t':
			dst = append(dst, '\\', 't')
		case character == '\n':
			dst = append(dst, '\\', 'n')
		case character == '\f':
			dst = append(dst, '\\', 'f')
		case character == '\r':
			dst = append(dst, '\\', 'r')
		case character < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', lowerHex(character>>4), lowerHex(character&0xf))
		default:
			dst = append(dst, character)
		}
	}
	return append(dst, '"')
}

func lowerHex(value byte) byte {
	if value < 10 {
		return '0' + value
	}
	return 'a' - 10 + value
}

func appendCanonicalInt(dst []byte, value int64) []byte {
	return strconv.AppendInt(dst, value, 10)
}

// appendCanonicalStringChecked rejects strings the PowerShell writer would
// refuse (unpaired UTF-16 surrogates surface in Go as invalid UTF-8), so a
// persisted artifact can never diverge from the legacy writer.
func appendCanonicalStringChecked(dst []byte, value string, name string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return dst, invalid("runner %s is not valid UTF-8; the canonical writer would reject it as an unpaired surrogate", name)
	}
	return appendCanonicalString(dst, value), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalRaw re-encodes an arbitrary decoded JSON document into canonical
// form, mirroring Get-BFHash(Read-BFJson ...) over a previously persisted
// queue input. Integers keep their exact value; any other number is refused
// because a task_queue document validated by Assert-BFRunnerQueue can only
// contain integers (a fractional number means the stored file is foreign).
func canonicalRaw(dst []byte, raw json.RawMessage) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "null":
		return append(dst, "null"...), nil
	case trimmed == "true":
		return append(dst, "true"...), nil
	case trimmed == "false":
		return append(dst, "false"...), nil
	case strings.HasPrefix(trimmed, `"`):
		var text string
		if err := json.Unmarshal([]byte(trimmed), &text); err != nil {
			return dst, invalid("stored queue document holds an invalid string")
		}
		return appendCanonicalStringChecked(dst, text, "queue document string")
	case strings.HasPrefix(trimmed, "{"):
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
			return dst, invalid("stored queue document holds an invalid object")
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dst = append(dst, '{')
		for _, key := range keys {
			var err error
			if dst, err = appendCanonicalStringChecked(dst, key, "queue document key"); err != nil {
				return dst, err
			}
			dst = append(dst, ':')
			if dst, err = canonicalRaw(dst, fields[key]); err != nil {
				return dst, err
			}
			dst = append(dst, ',')
		}
		if len(keys) > 0 {
			dst = dst[:len(dst)-1]
		}
		return append(dst, '}'), nil
	case strings.HasPrefix(trimmed, "["):
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return dst, invalid("stored queue document holds an invalid array")
		}
		dst = append(dst, '[')
		for _, item := range items {
			var err error
			if dst, err = canonicalRaw(dst, item); err != nil {
				return dst, err
			}
			dst = append(dst, ',')
		}
		if len(items) > 0 {
			dst = dst[:len(dst)-1]
		}
		return append(dst, ']'), nil
	default:
		number, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return dst, invalid("stored queue document holds a non-integer number")
		}
		return appendCanonicalInt(dst, number), nil
	}
}
