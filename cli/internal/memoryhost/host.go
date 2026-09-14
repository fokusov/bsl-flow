// Package memoryhost is the native replacement for the private PowerShell
// memory helper global/skills/1c-task/scripts/Invoke-BFNativeMemory.ps1. It
// reads one closed JSON request from stdin, performs one memory operation
// (bind / extract-attempt / extract-acceptance / projection) against the
// project-local .bsl-flow/memory store and always writes exactly one
// canonical envelope {schema_version, available, value, disabled_reason} to
// stdout. The process-level contract is preserved: the envelope carries
// every failure and the exit code is always 0.
package memoryhost

import (
	"bytes"
	"encoding/json"
	"io"

	"bsl-flow/cli/internal/strictjson"
)

const (
	nativeMemoryInputLimit  = 16 << 20 // $script:BFNativeMemoryInputLimit (16MB)
	nativeMemoryOutputLimit = 1 << 20  // $script:BFNativeMemoryOutputLimit (1MB)
	nativeMemoryErrorLimit  = 256
)

// PackageRoot locates the installed package (the directory that contains
// global/skills/1c-task), mirroring the PowerShell helper's $PSScriptRoot
// derivation. The wiring layer sets it for the in-process host; an empty
// root behaves like an installation without VERSION, package manifest and
// schemas, which yields the same (empty) fingerprint components on both
// engines.
var PackageRoot string

// RunMemoryHelper mirrors the whole Invoke-BFNativeMemory.ps1 process: read
// one request, run one operation, write one canonical envelope. It always
// returns exit code 0.
func RunMemoryHelper(stdin io.Reader, stdout io.Writer) int {
	return Run(stdin, stdout, PackageRoot)
}

// Run is RunMemoryHelper with an explicit package root.
func Run(stdin io.Reader, stdout io.Writer, packageRoot string) int {
	envelope := runOnce(stdin, packageRoot)
	writeEnvelope(stdout, envelope)
	return 0
}

func runOnce(stdin io.Reader, packageRoot string) map[string]any {
	data, err := readInputBytes(stdin)
	if err != nil {
		return newEnvelope(false, nil, errorText(err))
	}
	input, err := convertFromNativeMemoryInput(data)
	if err != nil {
		return newEnvelope(false, nil, errorText(err))
	}
	value, err := invokeNativeMemoryOperation(input, packageRoot)
	if err != nil {
		return newEnvelope(false, nil, errorText(err))
	}
	return newEnvelope(true, value, "")
}

// readInputBytes ports Read-BFNativeMemoryInputBytes: 64 KiB chunks and the
// 16 MiB bound checked before the chunk is accepted.
func readInputBytes(stdin io.Reader) ([]byte, error) {
	buffer := make([]byte, 65536)
	output := make([]byte, 0, 65536)
	for {
		read, err := stdin.Read(buffer)
		if read > 0 {
			if len(output)+read > nativeMemoryInputLimit {
				return nil, bfInvalid("Native memory input exceeds the 16 MiB limit.")
			}
			output = append(output, buffer[:read]...)
		}
		if err != nil {
			if err == io.EOF {
				return output, nil
			}
			return nil, err
		}
	}
}

// convertFromNativeMemoryInput ports ConvertFrom-BFNativeMemoryInput: strict
// single-document UTF-8 JSON with duplicate-key rejection. The shared
// strictjson scanner provides the same strict decoding as the packaged
// helper's Test-BFJsonSyntax + ConvertFrom-Json pair.
func convertFromNativeMemoryInput(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, bfInvalid("Native memory input is empty.")
	}
	text, ok := strictUTF8Decode(data)
	if !ok {
		return nil, bfInvalid("Native memory input is not valid UTF-8.")
	}
	text = stripBOM(text)
	if isNullOrWhiteSpace(text) {
		return nil, bfInvalid("Native memory input is empty.")
	}
	document, err := strictjson.Document([]byte(text))
	if err != nil {
		return nil, bfInvalid("Native memory input is not one valid JSON document.")
	}
	// The PowerShell helper requires the top-level kind to be an object, so
	// a valid array or scalar document gets the same single-document error.
	if len(document) == 0 || document[0] != '{' {
		return nil, bfInvalid("Native memory input is not one valid JSON document.")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, bfInvalid("Native memory input is not one valid JSON document.")
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil, bfInvalid("native memory input must be an object.")
	}
	return object, nil
}

// invokeNativeMemoryOperation ports Invoke-BFNativeMemoryOperation.
func invokeNativeMemoryOperation(input map[string]any, packageRoot string) (any, error) {
	validated, err := assertNativeMemoryInput(input)
	if err != nil {
		return nil, err
	}
	operation := psString(objectValue(input, "operation"))
	switch operation {
	case "bind":
		return opBind(validated.state, psString(objectValue(input, "stage")), validated.pendingFailure, validated.pendingFailureHash, packageRoot), nil
	case "extract-attempt":
		return opExtractAttempt(validated.state, asMapOr(objectValue(input, "result")), psString(objectValue(input, "result_hash")), packageRoot), nil
	case "extract-acceptance":
		return opExtractAcceptance(validated.state, asMapOr(objectValue(input, "receipt")), psString(objectValue(input, "receipt_hash")), packageRoot), nil
	case "projection":
		return opProjection(validated.state, asMapOr(objectValue(input, "next")), validated.pendingFailure, validated.pendingFailureHash, packageRoot), nil
	}
	return nil, bfInvalid("Unsupported native memory operation.")
}

// newEnvelope ports New-BFNativeMemoryEnvelope. The [string] parameter
// coercion in the original turns $null into "", so a success envelope
// carries disabled_reason "" (not null) on the wire.
func newEnvelope(available bool, value any, disabledReason string) map[string]any {
	return map[string]any{
		"schema_version":  1,
		"available":       available,
		"value":           value,
		"disabled_reason": disabledReason,
	}
}

// errorText ports Get-BFNativeMemoryErrorText.
func errorText(err error) string {
	message := ""
	if err != nil {
		message = err.Error()
	}
	if isNullOrWhiteSpace(message) {
		return "BF_BLOCKED: native memory helper failed."
	}
	if utf16Length(message) > nativeMemoryErrorLimit {
		message = trimEndUnicode(substringUTF16(message, nativeMemoryErrorLimit))
	}
	return message
}

// WriteDisabledEnvelope writes one canonical unavailable envelope with the
// given reason. The wiring layer uses it for host-level failures (for
// example an unresolvable bundle) that happen before a request is read, so
// the helper's process contract — exactly one envelope, always exit 0 —
// holds on every path.
func WriteDisabledEnvelope(stdout io.Writer, reason string) {
	writeEnvelope(stdout, newEnvelope(false, nil, reason))
}

// writeEnvelope ports Write-BFNativeMemoryEnvelope: canonical JSON, the
// 1 MiB output bound with its BF_BLOCKED replacement, and the constant
// fallback so the caller never receives a process exception.
func writeEnvelope(stdout io.Writer, envelope map[string]any) {
	data, err := canonicalBytes(envelope)
	if err == nil && len(data) > nativeMemoryOutputLimit {
		envelope = newEnvelope(false, nil, "BF_BLOCKED: native memory output exceeds the 1 MiB limit.")
		data, err = canonicalBytes(envelope)
	}
	if err == nil {
		if _, writeErr := stdout.Write(data); writeErr == nil {
			return
		}
	}
	const fallback = `{"available":false,"disabled_reason":"BF_BLOCKED: native memory output failed.","schema_version":1,"value":null}`
	_, _ = io.WriteString(stdout, fallback)
}
