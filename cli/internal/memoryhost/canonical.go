package memoryhost

// This file ports the canonical JSON encoder and SHA-256 hashing from
// Task.Storage.ps1 (ConvertTo-BFJsonString, ConvertTo-BFCanonicalNumber,
// Get-BFCanonicalJson, Get-BFHash). Persisted artifacts (events, index,
// envelopes, record identities) hash these exact bytes on both engines.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"
)

const jsonMaximumDepth = 100 // $script:BFJsonMaximumDepth

// canonicalBytes encodes a JSON value with deterministic ordinal key ordering,
// no insignificant whitespace and raw UTF-8, so its bytes can be hashed.
func canonicalBytes(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeCanonicalValue(&buffer, value, 0); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// canonicalText is canonicalBytes for callers that embed the result in text.
func canonicalText(value any) (string, error) {
	data, err := canonicalBytes(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// hashValue returns the lowercase SHA-256 of the canonical JSON encoding,
// mirroring Get-BFHash.
func hashValue(value any) (string, error) {
	data, err := canonicalBytes(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonicalValue(buffer *bytes.Buffer, value any, depth int) error {
	if depth > jsonMaximumDepth {
		return bfInvalid("JSON value exceeds the maximum nesting depth.")
	}
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
		return writeCanonicalString(buffer, typed)
	case json.Number:
		if err := validCanonicalNumber(string(typed)); err != nil {
			return err
		}
		buffer.WriteString(string(typed))
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		// PowerShell renders doubles with the round-trip G17 format; Go's
		// shortest round-trip representation matches it for every value the
		// memory plane persists (counts and revisions are integers).
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return bfInvalid("Non-finite numbers are not valid JSON values.")
		}
		buffer.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
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
			if err := writeCanonicalValue(buffer, item, depth+1); err != nil {
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
			if err := writeCanonicalString(buffer, key); err != nil {
				return err
			}
			buffer.WriteByte(':')
			if err := writeCanonicalValue(buffer, typed[key], depth+1); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return writeCanonicalReflected(buffer, value, depth)
	}
	return nil
}

func writeCanonicalReflected(buffer *bytes.Buffer, value any, depth int) error {
	if depth > jsonMaximumDepth {
		return bfInvalid("JSON value exceeds the maximum nesting depth.")
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		if reflected.Kind() != reflect.Array && reflected.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		buffer.WriteByte('[')
		for index := 0; index < reflected.Len(); index++ {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalValue(buffer, reflected.Index(index).Interface(), depth+1); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	case reflect.Map:
		if reflected.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		keys := make([]string, 0, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key, ok := iterator.Key().Interface().(string)
			if !ok {
				return bfInvalid("JSON object keys must be strings.")
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalString(buffer, key); err != nil {
				return err
			}
			buffer.WriteByte(':')
			if err := writeCanonicalValue(buffer, reflected.MapIndex(reflect.ValueOf(key)).Interface(), depth+1); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	default:
		return bfInvalid("Unsupported JSON value type: %T.", value)
	}
}

// writeCanonicalString ports ConvertTo-BFJsonString: the five short escapes,
// \u00xx for the remaining control characters (lowercase hex) and raw UTF-8.
// PowerShell cannot hold invalid UTF-16 in its strings; invalid UTF-8 gets the
// same contract error instead of being silently encoded.
func writeCanonicalString(buffer *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return bfInvalid("String contains an unpaired UTF-16 surrogate.")
	}
	buffer.WriteByte('"')
	for index := 0; index < len(value); {
		character := value[index]
		if character < 0x20 || character == '"' || character == '\\' {
			switch character {
			case '\b':
				buffer.WriteString(`\b`)
			case '\t':
				buffer.WriteString(`\t`)
			case '\n':
				buffer.WriteString(`\n`)
			case '\f':
				buffer.WriteString(`\f`)
			case '\r':
				buffer.WriteString(`\r`)
			case '"':
				buffer.WriteString(`\"`)
			case '\\':
				buffer.WriteString(`\\`)
			default:
				fmt.Fprintf(buffer, `\u%04x`, character)
			}
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == utf8.RuneError && size == 1 {
			return bfInvalid("String contains an unpaired UTF-16 surrogate.")
		}
		buffer.WriteString(value[index : index+size])
		index += size
	}
	buffer.WriteByte('"')
	return nil
}

var canonicalNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func validCanonicalNumber(text string) error {
	if !canonicalNumberPattern.MatchString(text) {
		return bfInvalid("Unsupported numeric type: %s.", text)
	}
	return nil
}
