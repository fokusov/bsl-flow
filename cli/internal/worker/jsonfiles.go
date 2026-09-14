package worker

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/strictjson"
)

// sprintf is the local formatting entry point so the classification helpers
// stay single-sourced.
func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// errorsAs aliases errors.As for the few call sites that inspect native
// decoder error types.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

func randomByte() (byte, error) {
	var buffer [1]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return 0, err
	}
	return buffer[0], nil
}

// JSON/hash plumbing shared by the worker adapters. Every persisted file goes
// through repository.Canonical (the native port of Get-BFCanonicalJson:
// key-sorted members, minimal escaping, raw UTF-8), so receipts serialize with
// identical field names and ordering semantics as the PowerShell adapters.

// repositoryCanonical exposes the canonical encoder (the native port of
// Get-BFCanonicalJson) to package tests.
func repositoryCanonical(value any) ([]byte, error) { return repository.Canonical(value) }

// hashValue mirrors Get-BFHash: the lowercase SHA-256 of the canonical JSON
// encoding of the value.
func hashValue(value any) (string, error) {
	hash, err := repository.Hash(value)
	if err != nil {
		return "", invalid("%v", err)
	}
	return hash, nil
}

// hashValueEqual compares two values by canonical hash (Get-BFHash equality).
func hashValueEqual(left, right any) (bool, error) {
	leftHash, err := hashValue(left)
	if err != nil {
		return false, err
	}
	rightHash, err := hashValue(right)
	if err != nil {
		return false, err
	}
	return leftHash == rightHash, nil
}

// fileHash mirrors Get-BFFileHash: the lowercase SHA-256 of the file bytes.
// A missing file is the BF_INVALID refusal of the PowerShell original.
func fileHash(path string) (string, error) {
	resolved, err := workerSafePath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", invalid("File does not exist: %s", resolved)
		}
		return "", invalid("%v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// readJSONObjectFile mirrors Read-BFJson: one bounded strict JSON object
// document under the controller bounds (16 MiB, valid UTF-8, BOM tolerated,
// trailing data and duplicate keys — including case-only duplicates —
// rejected, unknown members retained like ConvertFrom-Json).
func readJSONObjectFile(path string) (map[string]any, error) {
	resolved, err := workerSafePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, invalid("JSON file does not exist: %s", resolved)
	}
	if info.Size() > 16<<20 {
		return nil, invalid("JSON file exceeds the maximum allowed size.")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, invalid("Cannot read JSON file: %v", err)
	}
	if !utf8.Valid(data) {
		return nil, invalid("JSON file is not valid UTF-8.")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	// Test-BFJsonSyntax phase: duplicate object keys (case-insensitive),
	// trailing data and depth bounds.
	document, err := strictjson.Document([]byte(text))
	if err != nil {
		return nil, invalid("%v", err)
	}
	// ConvertFrom-Json materialization phase (exact-duplicate keys were
	// already rejected above; unknown fields are retained).
	object, err := parseJSONObject(document)
	if err != nil {
		return nil, invalid("Cannot materialize JSON object: %v", err)
	}
	return object, nil
}

// parseJSONScalarDocument decodes exactly one top-level JSON value of any
// kind (object, array or scalar), mirroring ConvertFrom-Json materialization
// used for payload strings.
func parseJSONScalarDocument(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("unexpected data after JSON value")
	}
	return value, nil
}

// writeJSONFile mirrors Write-BFJson: canonical bytes published atomically.
// The legacy task-ownership lock machinery of the controller stays with the
// controller; the worker package only writes inside attempt directories it
// already owns.
func writeJSONFile(path string, value any, replace bool) error {
	resolved, err := workerSafePath(path)
	if err != nil {
		return err
	}
	data, err := repository.Canonical(value)
	if err != nil {
		return invalid("%v", err)
	}
	if err := repository.AtomicWrite(resolved, data, replace); err != nil {
		if !replace && strings.HasPrefix(err.Error(), "refusing to overwrite existing file") {
			return conflict("Refusing to overwrite JSON file: %s", resolved)
		}
		return conflict("Could not publish JSON file: %v", err)
	}
	return nil
}

// conflict classifies a BF_CONFLICT refusal (Write-BFJson publish conflicts).
func conflict(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_CONFLICT", Message: sprintf(format, args...)}
}

// assertFields mirrors Assert-BFFields with its exact diagnostics
// (Task.Contracts.ps1:8-18).
func assertFields(value any, required, optional []string, name string) (map[string]any, error) {
	object, ok := asObject(value)
	if !ok {
		return nil, invalid("%s must be an object.", name)
	}
	for _, key := range required {
		if _, present := object[key]; !present {
			return nil, invalid("%s.%s is required.", name, key)
		}
	}
	for key := range object {
		allowed := false
		for _, candidate := range required {
			if candidate == key {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, candidate := range optional {
				if candidate == key {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return nil, invalid("unknown field %s.%s.", name, key)
		}
	}
	return object, nil
}

// assertTextValue mirrors Assert-BFText: a non-empty, non-whitespace string
// under the 262144 character limit.
func assertTextValue(value any, name string) error {
	text, ok := asString(value)
	if !ok || strings.TrimSpace(text) == "" || len([]rune(text)) > 262144 {
		return invalid("invalid %s.", name)
	}
	return nil
}

// jsonQuote embeds a string as a single JSON string literal, mirroring
// ConvertTo-Json -InputObject <string> -Compress for the value domain the
// adapters serialize (paths, efforts, tool names): minimal escaping with a
// raw UTF-8 payload. Windows paths cannot contain the HTML-sensitive
// characters PowerShell additionally escapes, so the bytes match for every
// value the adapters embed.
func jsonQuote(value string) string {
	return string(appendCanonicalString(nil, value))
}

// forwardSlash normalizes a Windows path to forward slashes (the adapters'
// .Replace('\','/') convention).
func forwardSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// guidN returns a fresh lowercase GUID in the "N" format (32 hex digits, no
// dashes), mirroring [guid]::NewGuid().ToString('N').
func guidN() (string, error) {
	var bytes [16]byte
	for index := range bytes {
		byteValue, err := randomByte()
		if err != nil {
			return "", err
		}
		bytes[index] = byteValue
	}
	// RFC 4122 version/variant bits, like Guid.NewGuid.
	bytes[7] = (bytes[7] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return hex.EncodeToString(bytes[:]), nil
}

// roundTripUTCTime formats a timestamp like [DateTime]::UtcNow.ToString('o').
func roundTripUTCTime(moment time.Time) string {
	return moment.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// nowTime returns the wall clock; tests may not override it, receipts embed it
// exactly like the PowerShell adapters embed [DateTime]::UtcNow.
func nowTime() time.Time {
	return time.Now()
}
