// Package strictjson decodes exactly one JSON object/array document with the
// provider-grade strictness shared by the host adapters and the native stage
// host: no trailing data, no duplicate object members (including case-only
// duplicates), bounded nesting, member counts and key sizes.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Bounds mirror the packaged provider contract. They keep validation stack-
// and allocation-bounded for untrusted input while leaving room for ordinary
// source manifests and dependency maps.
const (
	MaxDepth         = 128
	MaxObjectMembers = 100000
	MaxKeyBytes      = 1 << 20
)

// Document validates and returns one trimmed strict JSON object/array document.
func Document(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(value)) == 0 {
		return nil, errors.New("empty JSON document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON document")
		}
		return nil, fmt.Errorf("trailing data: %w", err)
	}
	if first := bytes.TrimSpace(value); len(first) == 0 || (first[0] != '{' && first[0] != '[') {
		return nil, errors.New("JSON document must be an object or array")
	}
	if err := RejectDuplicateKeys(value); err != nil {
		return nil, err
	}
	return append([]byte(nil), bytes.TrimSpace(value)...), nil
}

// RejectDuplicateKeys walks the decoded JSON token stream and rejects
// duplicate object members, including members which differ only by case. The
// latter matters because encoding/json matches struct fields
// case-insensitively and would otherwise accept an ambiguous document.
func RejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON document")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func walkValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	if depth >= MaxDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", MaxDepth)
	}
	switch delim {
	case '{':
		keys := make(map[string]string, 8)
		memberCount := 0
		for decoder.More() {
			memberCount++
			if memberCount > MaxObjectMembers {
				return fmt.Errorf("JSON object exceeds %d members", MaxObjectMembers)
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object member name is not a string")
			}
			if len(key) > MaxKeyBytes {
				return fmt.Errorf("JSON object member name exceeds %d bytes", MaxKeyBytes)
			}
			folded := foldKey(key)
			if prior, exists := keys[folded]; exists && strings.EqualFold(prior, key) {
				return fmt.Errorf("duplicate JSON object member %q", key)
			}
			keys[folded] = key
			if err := walkValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("malformed JSON delimiter")
	}
	return nil
}

// foldKey returns a canonical representative for Unicode simple-fold
// equivalence, the same equivalence relation used by strings.EqualFold. It
// lets duplicate detection stay O(number of members) while retaining the
// case-insensitive collision policy.
func foldKey(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, runeValue := range value {
		canonical := runeValue
		for next := unicode.SimpleFold(runeValue); next != runeValue; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
