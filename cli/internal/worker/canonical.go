package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// appendCanonicalString appends the canonical JSON encoding of a string:
// minimal escaping with raw UTF-8 payload, mirroring ConvertTo-BFJsonString
// (Task.Storage.ps1:16-50). Bytes are escaped deterministically without a
// validity requirement, so callers that must reject invalid UTF-8 (like the
// receipt writer, whose PS counterpart throws on unpaired surrogates) check
// utf8.ValidString before encoding.
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

// sha256Hex returns the lowercase hex SHA-256 of canonical bytes, mirroring
// Get-BFHash (Task.Storage.ps1:124-132).
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// appendUsageCanonical writes the usage object with key-sorted members, the
// optional counters omitted when the host omitted them.
func appendUsageCanonical(dst []byte, usage *Usage) []byte {
	if usage == nil {
		return append(dst, `null`...)
	}
	dst = append(dst, '{')
	if usage.CacheWriteInputTokens != nil {
		dst = append(dst, `"cache_write_input_tokens":`...)
		dst = appendCanonicalInt(dst, *usage.CacheWriteInputTokens)
		dst = append(dst, ',')
	}
	dst = append(dst, `"cached_input_tokens":`...)
	dst = appendCanonicalInt(dst, usage.CachedInputTokens)
	dst = append(dst, `,"input_tokens":`...)
	dst = appendCanonicalInt(dst, usage.InputTokens)
	dst = append(dst, `,"output_tokens":`...)
	dst = appendCanonicalInt(dst, usage.OutputTokens)
	if usage.ReasoningOutputTokens != nil {
		dst = append(dst, `,"reasoning_output_tokens":`...)
		dst = appendCanonicalInt(dst, *usage.ReasoningOutputTokens)
	}
	return append(dst, '}')
}
