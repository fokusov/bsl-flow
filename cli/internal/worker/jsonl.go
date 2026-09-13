package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// maxJSONDepth mirrors $script:BFJsonMaximumDepth (Task.Storage.ps1:5); the
// PowerShell adapters parse JSONL records with -Depth 100.
const maxJSONDepth = 100

// parseJSONObject decodes exactly one top-level JSON object from a single
// JSONL record. It mirrors the leniency profile of the PowerShell parsers:
//
//   - unknown fields are tolerated on every level (ConvertFrom-Json keeps
//     them; the adapters only project the fields they consume, and rollout
//     fixtures carry timestamp/ordinal/originator/cli_version extras), so
//     json.Decoder.DisallowUnknownFields is deliberately NOT used;
//   - duplicate object keys are rejected (PowerShell 7 ConvertFrom-Json
//     rejects duplicated keys; the canonical writer is even stricter and
//     detects case-insensitive duplicates, Get-BFCanonicalJson
//     Task.Storage.ps1:93-98 — here exact ordinal duplicates are rejected,
//     matching the parser the adapters actually use);
//   - trailing data after the object is rejected, mirroring
//     Test-BFJsonSyntax "Unexpected data after JSON value"
//     (Task.Storage.ps1:606);
//   - numbers keep their literal precision via json.Number.
func parseJSONObject(line []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("unexpected data after JSON value")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("top-level JSON value must be an object")
	}
	return object, nil
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, invalid("JSON exceeds the maximum nesting depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, invalid("malformed JSON: %v", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, invalid("malformed JSON: %v", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, invalid("JSON object keys must be strings")
			}
			if _, exists := object[key]; exists {
				return nil, invalid("duplicate JSON object key: %s", key)
			}
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, invalid("malformed JSON: %v", err)
		}
		return object, nil
	case '[':
		array := make([]any, 0, 4)
		for decoder.More() {
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, invalid("malformed JSON: %v", err)
		}
		return array, nil
	default:
		return nil, invalid("unexpected JSON delimiter")
	}
}

func asObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func asString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

// asCounter extracts a non-negative integer counter. It mirrors the usage
// validation of Read-BFProfiledCodexEvents (ProfiledCodex.ps1:193-196): the
// value must be an integral number — booleans, fractional values and numeric
// strings are all rejected.
func asCounter(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	if err != nil || integer < 0 {
		return 0, false
	}
	return integer, true
}

// asText enforces the Assert-BFText contract (Task.Contracts.ps1:27-30): a
// non-empty, non-whitespace string.
func asText(value any, name string) error {
	text, ok := asString(value)
	if !ok || strings.TrimSpace(text) == "" {
		return invalid("invalid %s", name)
	}
	return nil
}
