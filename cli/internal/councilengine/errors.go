package councilengine

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// errors.go carries the BF_* error taxonomy of the PowerShell council
// scripts, the hashed-JSON helpers and the atomic artifact writer
// (Write-BSLFlowJsonAtomic) with its exact byte shape: pretty ConvertTo-Json
// bytes plus one trailing CRLF, published through a same-directory temporary.

// KindError is a classified council failure. Kind is one of the BF_ classes;
// Error() renders the full "BF_KIND: message" text the PowerShell surface
// produces.
type KindError struct {
	Kind    string
	Message string
}

func (e *KindError) Error() string { return e.Kind + ": " + e.Message }

// BF_* classes used by the council engine.
const (
	KindInvalid = "BF_INVALID"
	KindBlocked = "BF_BLOCKED"
)

// Dispatch failure markers the cycle classifies into terminal member states
// (Invoke-CouncilReview.ps1:372-374). The transport glue returns errors whose
// message carries one of these markers.
const (
	MarkerBeforeAcceptance = "BF_FAILED_BEFORE_ACCEPTANCE"
	MarkerInvalidResponse  = "BF_INVALID_RESPONSE"
	MarkerNotDispatched    = "BF_NOT_DISPATCHED"
)

func invalid(format string, args ...any) error {
	return &KindError{Kind: KindInvalid, Message: fmt.Sprintf(format, args...)}
}

func blocked(format string, args ...any) error {
	return &KindError{Kind: KindBlocked, Message: fmt.Sprintf(format, args...)}
}

// sha256Hex returns the lowercase hex digest of bytes.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashPSJSON hashes the ConvertTo-Json bytes of value (the PowerShell
// `$value | ConvertTo-Json -Depth depth` input of Get-BSLFlowBytesSha256).
func hashPSJSON(value any, depth int) (string, error) {
	data, err := convertToJSON(value, depth)
	if err != nil {
		return "", invalid("%v", err)
	}
	return sha256Hex(data), nil
}

// guidN mirrors [guid]::NewGuid().ToString('N'): 32 lowercase hex digits with
// RFC 4122 version/variant bits.
func guidN() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", blocked("guid source unavailable: %v", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return hex.EncodeToString(bytes[:]), nil
}

// roundTripUTCTime formats a UTC timestamp like [DateTime]::UtcNow.ToString('o')
// (seven fractional digits).
func roundTripUTCTime(moment time.Time) string {
	return moment.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// writeBSLFlowJSONAtomic ports Write-BSLFlowJsonAtomic: ConvertTo-Json -Depth
// 20 bytes (CRLF lines) plus one trailing CRLF, UTF-8 no BOM, published
// atomically through a same-dot-directory temporary with a GUID suffix.
func writeBSLFlowJSONAtomic(path string, value any) error {
	data, err := convertToJSON(value, 20)
	if err != nil {
		return invalid("%v", err)
	}
	data = append(data, '\r', '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return blocked("%v", err)
	}
	guid, err := guidN()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+filepath.Base(path)+"."+guid+".tmp")
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return blocked("%v", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return blocked("%v", err)
	}
	return nil
}

// readJSONObject reads one bounded strict JSON object document. Materialized
// values keep number literals (json.Number) like ConvertFrom-Json; object
// member order for *ordered consumers is the file order through
// decodeOrderedDocument.
func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, invalid("JSON file does not exist: %s", path)
	}
	if len(data) > 16<<20 {
		return nil, invalid("JSON file exceeds the maximum allowed size.")
	}
	if !utf8Valid(data) {
		return nil, invalid("JSON file is not valid UTF-8.")
	}
	return decodeObjectDocument(data)
}

func utf8Valid(data []byte) bool {
	return utf8.Valid(data)
}

// decodeObjectDocument parses exactly one top-level JSON object with number
// literals preserved.
func decodeObjectDocument(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(trimBOM(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid("Cannot materialize JSON object: %v", err)
	}
	if _, err := decoder.Token(); err == nil {
		return nil, invalid("Cannot materialize JSON object: trailing data.")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("Cannot materialize JSON object: top-level value is not an object.")
	}
	return object, nil
}

// decodeOrderedDocument parses one JSON document into *ordered trees
// preserving member order, so the engine can re-serialize artifacts exactly
// like the PowerShell round-trip (ConvertFrom-Json keeps property order in
// PSCustomObject).
func decodeOrderedDocument(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(trimBOM(data)))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, invalid("Cannot materialize JSON: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("Cannot materialize JSON: trailing data.")
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := newOrdered()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key must be a string")
				}
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				object.set(key, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			items := []any{}
			for decoder.More() {
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return items, nil
		}
		return nil, errors.New("unexpected JSON delimiter")
	case string:
		return typed, nil
	case json.Number:
		return typed, nil
	case bool:
		return typed, nil
	case nil:
		return nil, nil
	default:
		return nil, errors.New("unexpected JSON token")
	}
}

// DecodeOrdered parses one JSON document into order-preserving *ordered trees
// (exported for sibling packages that must re-serialize model output).
func DecodeOrdered(data []byte) (any, error) { return decodeOrderedDocument(data) }

// readOrderedObject reads one JSON object document preserving member order,
// mirroring ConvertFrom-Json (which keeps property order in PSCustomObject).
// Attempt/result files must round-trip their binding order for hashing.
func readOrderedObject(path string) (*ordered, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, invalid("JSON file does not exist: %s", path)
	}
	if len(data) > 16<<20 {
		return nil, invalid("JSON file exceeds the maximum allowed size.")
	}
	if !utf8Valid(data) {
		return nil, invalid("JSON file is not valid UTF-8.")
	}
	value, err := decodeOrderedDocument(data)
	if err != nil {
		return nil, err
	}
	object, ok := value.(*ordered)
	if !ok {
		return nil, invalid("Top-level JSON value must be an object.")
	}
	return object, nil
}

func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// strictPSUTF8Decode mirrors [Text.UTF8Encoding]::new($false, $true).GetString:
// invalid UTF-8 is an error, not a replacement.
func strictPSUTF8Decode(data []byte) (string, error) {
	if !utf8ValidStrict(data) {
		return "", errors.New("not valid UTF-8")
	}
	return string(data), nil
}

func utf8ValidStrict(data []byte) bool {
	return utf8Valid(data) && !strings.ContainsRune(string(data), 0xFFFD)
}

// finiteNonNegative mirrors the council numeric guard.
func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
