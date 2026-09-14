package repository

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	nativeTransportSchemaVersion = int64(1)

	nativeTransportReceiptFile = "transport.json"
	nativeTransportStdoutFile  = "transport.stdout"
	nativeTransportStderrFile  = "transport.stderr"
)

var nativeTransportEnvelopeFields = map[string]bool{
	"schema_version": true,
	"receipt":        true,
	"stdout_path":    true,
	"stdout_sha256":  true,
	"stdout_size":    true,
	"stderr_path":    true,
	"stderr_sha256":  true,
	"stderr_size":    true,
}

var nativeTransportReceiptFields = map[string]bool{
	"schema_version": true,
	"executable":     true,
	"argv":           true,
	"started":        true,
	"terminal":       true,
	"exit_code":      true,
	"stop_reason":    true,
	"stdout_bytes":   true,
	"stderr_bytes":   true,
	"stdout_sha256":  true,
	"stderr_sha256":  true,
	"duration_ms":    true,
}

var nativeTransportStopReasons = map[string]bool{
	"":           true,
	"stdin":      true,
	"start":      true,
	"timeout":    true,
	"cancel":     true,
	"size-limit": true,
	"exit":       true,
}

// retainNativeTransport stores the host-owned process receipt and the exact
// bounded streams returned by the process runner. The receipt may describe a
// failed or interrupted invocation; successful terminal observations are
// checked separately by validateNativeTransport once the provider output is
// available. Every file is immutable and retries must supply identical bytes.
func retainNativeTransport(attemptPath string, transport *ProviderTransportEvidence) error {
	if transport == nil {
		return blocked("native transport evidence is missing")
	}
	if _, err := SafePath(attemptPath); err != nil {
		return blocked("native transport attempt path is unsafe: %v", err)
	}
	if err := validateNativeTransportEvidence(transport); err != nil {
		return err
	}

	stdoutPath := filepath.Join(attemptPath, nativeTransportStdoutFile)
	stderrPath := filepath.Join(attemptPath, nativeTransportStderrFile)
	if err := writeImmutableTransportBytes(stdoutPath, transport.Stdout); err != nil {
		return blocked("native transport stdout could not be retained: %v", err)
	}
	if err := writeImmutableTransportBytes(stderrPath, transport.Stderr); err != nil {
		return blocked("native transport stderr could not be retained: %v", err)
	}

	envelope := map[string]any{
		"schema_version": nativeTransportSchemaVersion,
		"receipt":        transport.Receipt,
		"stdout_path":    nativeTransportStdoutFile,
		"stdout_sha256":  fileSHA256(transport.Stdout),
		"stdout_size":    int64(len(transport.Stdout)),
		"stderr_path":    nativeTransportStderrFile,
		"stderr_sha256":  fileSHA256(transport.Stderr),
		"stderr_size":    int64(len(transport.Stderr)),
	}
	if _, err := writeImmutableJSON(filepath.Join(attemptPath, nativeTransportReceiptFile), envelope); err != nil {
		return blocked("native transport receipt could not be retained: %v", err)
	}
	return nil
}

// validateNativeTransport verifies that a retained successful provider result
// came from the exact host invocation recorded beside the attempt. It is used
// by run, resume and record; a result without this receipt is never terminal.
func validateNativeTransport(attemptPath string, observation ExecuteObservation, engine EngineIdentity) error {
	_, transport, err := readNativeTransport(attemptPath)
	if err != nil {
		return err
	}
	if err := validateNativeTransportSuccess(transport.Receipt); err != nil {
		return err
	}
	if err := validateNativeTransportIdentity(transport.Receipt, engine); err != nil {
		return err
	}

	actualObject, err := DecodeObject(transport.Stdout)
	if err != nil {
		return blocked("native transport stdout is not a provider observation: %v", err)
	}
	actualCanonical, err := Canonical(actualObject)
	if err != nil {
		return blocked("native transport stdout could not be canonicalized: %v", err)
	}
	// The provider entrypoint emits canonical JSON. Requiring the retained bytes
	// to be canonical also rejects duplicate keys, a BOM, and trailing data that
	// DecodeObject alone would otherwise normalize away.
	if !bytes.Equal(actualCanonical, transport.Stdout) {
		return blocked("native transport stdout is not the exact canonical observation")
	}

	expectedObject := toExecuteObservation(observation)
	actualHash, err := Hash(actualObject)
	if err != nil {
		return blocked("native transport observation hash could not be computed: %v", err)
	}
	expectedHash, err := Hash(expectedObject)
	if err != nil {
		return blocked("native execute observation hash could not be computed: %v", err)
	}
	if actualHash != expectedHash {
		return blocked("native transport stdout does not match the retained provider observation")
	}
	return nil
}

// validateNativeTransportEvidence checks the receipt against the bytes
// captured by the trusted Go process. It deliberately accepts non-terminal
// receipts so that a failed invocation can be retained for reconciliation.
func validateNativeTransportEvidence(transport *ProviderTransportEvidence) error {
	if transport == nil {
		return blocked("native transport evidence is missing")
	}
	return validateNativeTransportReceipt(transport.Receipt, transport.Stdout, transport.Stderr)
}

func validateNativeTransportReceipt(receipt map[string]any, stdout, stderr []byte) error {
	if receipt == nil {
		return blocked("native transport receipt is missing")
	}
	if len(receipt) != len(nativeTransportReceiptFields) {
		return invalid("native transport receipt contains unsupported or missing fields")
	}
	for field := range receipt {
		if !nativeTransportReceiptFields[field] {
			return invalid("native transport receipt contains unsupported field %s", field)
		}
	}

	if value, ok := nativeTransportInteger(receipt["schema_version"]); !ok || value != nativeTransportSchemaVersion {
		return invalid("native transport receipt schema_version must be 1")
	}
	executable, ok := receipt["executable"].(string)
	if !ok || strings.TrimSpace(executable) == "" || strings.IndexByte(executable, 0) >= 0 {
		return invalid("native transport receipt executable is invalid")
	}
	if _, ok := nativeTransportStrings(receipt["argv"]); !ok {
		return invalid("native transport receipt argv is invalid")
	}
	started, ok := receipt["started"].(bool)
	if !ok {
		return invalid("native transport receipt started is invalid")
	}
	terminal, ok := receipt["terminal"].(bool)
	if !ok {
		return invalid("native transport receipt terminal is invalid")
	}
	stopReason, ok := receipt["stop_reason"].(string)
	if !ok || !nativeTransportStopReasons[stopReason] {
		return invalid("native transport receipt stop_reason is invalid")
	}

	stdoutBytes, ok := nativeTransportInteger(receipt["stdout_bytes"])
	if !ok || stdoutBytes < 0 || stdoutBytes > int64(len(stdout)) || stdoutBytes != int64(len(stdout)) {
		return blocked("native transport receipt stdout_bytes does not match captured stdout")
	}
	stderrBytes, ok := nativeTransportInteger(receipt["stderr_bytes"])
	if !ok || stderrBytes < 0 || stderrBytes > int64(len(stderr)) || stderrBytes != int64(len(stderr)) {
		return blocked("native transport receipt stderr_bytes does not match captured stderr")
	}
	if stdoutBytes > int64(maxJSONBytes) || stderrBytes > int64(maxJSONBytes) {
		return blocked("native transport stream exceeds the maximum retained size")
	}
	stdoutHash, ok := receipt["stdout_sha256"].(string)
	if !ok || !isSHA256(stdoutHash) || stdoutHash != fileSHA256(stdout) {
		return blocked("native transport receipt stdout_sha256 does not match captured stdout")
	}
	stderrHash, ok := receipt["stderr_sha256"].(string)
	if !ok || !isSHA256(stderrHash) || stderrHash != fileSHA256(stderr) {
		return blocked("native transport receipt stderr_sha256 does not match captured stderr")
	}
	duration, ok := nativeTransportInteger(receipt["duration_ms"])
	if !ok || duration < 0 {
		return invalid("native transport receipt duration_ms is invalid")
	}

	var exitCode *int64
	if raw := receipt["exit_code"]; raw != nil {
		value, ok := nativeTransportInteger(raw)
		if !ok || value < 0 || value > int64(^uint(0)>>1) {
			return invalid("native transport receipt exit_code is invalid")
		}
		exitCode = &value
	}
	if !started {
		if terminal || exitCode != nil {
			return blocked("native transport receipt has process state inconsistent with started=false")
		}
	}
	if terminal {
		if !started || exitCode == nil || (stopReason != "" && stopReason != "exit") {
			return blocked("native transport receipt terminal state is inconsistent")
		}
	} else if stopReason == "" {
		return blocked("native transport receipt without terminal state has no stop_reason")
	}
	if exitCode != nil {
		// A failed write to an already-exited process can produce waitErr with
		// exit code 0. The native runner marks that non-terminal receipt as
		// `exit`; only a terminal zero exit is required to have no stop reason.
		if *exitCode == 0 && stopReason != "" && (terminal || stopReason != "exit") {
			return blocked("native transport receipt has exit_code 0 with a stop_reason")
		}
		if *exitCode != 0 && stopReason != "exit" {
			return blocked("native transport receipt has a nonzero exit_code without exit stop_reason")
		}
	}
	return nil
}

func validateNativeTransportSuccess(receipt map[string]any) error {
	started, _ := receipt["started"].(bool)
	terminal, _ := receipt["terminal"].(bool)
	stopReason, _ := receipt["stop_reason"].(string)
	exitCode, ok := nativeTransportInteger(receipt["exit_code"])
	if !started || !terminal || stopReason != "" || !ok || exitCode != 0 {
		return blocked("native provider has no successful terminal transport receipt")
	}
	return nil
}

func readNativeTransport(attemptPath string) (map[string]any, *ProviderTransportEvidence, error) {
	if _, err := SafePath(attemptPath); err != nil {
		return nil, nil, blocked("native transport attempt path is unsafe: %v", err)
	}
	receiptPath := filepath.Join(attemptPath, nativeTransportReceiptFile)
	receiptBytes, err := ReadFileBytes(receiptPath)
	if err != nil {
		return nil, nil, blocked("native transport receipt is missing or unreadable: %v", err)
	}
	envelope, err := DecodeObject(receiptBytes)
	if err != nil {
		return nil, nil, blocked("native transport receipt is invalid: %v", err)
	}
	canonicalEnvelope, err := Canonical(envelope)
	if err != nil || !bytes.Equal(canonicalEnvelope, receiptBytes) {
		return nil, nil, blocked("native transport receipt is not canonical")
	}
	if len(envelope) != len(nativeTransportEnvelopeFields) {
		return nil, nil, invalid("native transport envelope contains unsupported or missing fields")
	}
	for field := range envelope {
		if !nativeTransportEnvelopeFields[field] {
			return nil, nil, invalid("native transport envelope contains unsupported field %s", field)
		}
	}
	version, ok := nativeTransportInteger(envelope["schema_version"])
	if !ok || version != nativeTransportSchemaVersion {
		return nil, nil, invalid("native transport envelope schema_version must be 1")
	}
	receipt, ok := envelope["receipt"].(map[string]any)
	if !ok || receipt == nil {
		return nil, nil, invalid("native transport envelope receipt must be an object")
	}
	stdoutPath, ok := envelope["stdout_path"].(string)
	if !ok || stdoutPath != nativeTransportStdoutFile {
		return nil, nil, invalid("native transport envelope stdout_path is invalid")
	}
	stderrPath, ok := envelope["stderr_path"].(string)
	if !ok || stderrPath != nativeTransportStderrFile {
		return nil, nil, invalid("native transport envelope stderr_path is invalid")
	}

	stdoutPath = filepath.Join(attemptPath, stdoutPath)
	stderrPath = filepath.Join(attemptPath, stderrPath)
	stdout, err := ReadFileBytes(stdoutPath)
	if err != nil {
		return nil, nil, blocked("native transport stdout is missing or unreadable: %v", err)
	}
	stderr, err := ReadFileBytes(stderrPath)
	if err != nil {
		return nil, nil, blocked("native transport stderr is missing or unreadable: %v", err)
	}
	stdoutHash, ok := envelope["stdout_sha256"].(string)
	if !ok || !isSHA256(stdoutHash) || stdoutHash != fileSHA256(stdout) {
		return nil, nil, blocked("native transport envelope stdout hash does not match bytes")
	}
	stderrHash, ok := envelope["stderr_sha256"].(string)
	if !ok || !isSHA256(stderrHash) || stderrHash != fileSHA256(stderr) {
		return nil, nil, blocked("native transport envelope stderr hash does not match bytes")
	}
	stdoutSize, ok := nativeTransportInteger(envelope["stdout_size"])
	if !ok || stdoutSize != int64(len(stdout)) {
		return nil, nil, blocked("native transport envelope stdout size does not match bytes")
	}
	stderrSize, ok := nativeTransportInteger(envelope["stderr_size"])
	if !ok || stderrSize != int64(len(stderr)) {
		return nil, nil, blocked("native transport envelope stderr size does not match bytes")
	}
	transport := &ProviderTransportEvidence{Receipt: receipt, Stdout: stdout, Stderr: stderr}
	if err := validateNativeTransportEvidence(transport); err != nil {
		return nil, nil, err
	}
	return envelope, transport, nil
}

func writeImmutableTransportBytes(path string, data []byte) error {
	if existing, err := ReadFileBytes(path); err == nil {
		if !bytes.Equal(existing, data) {
			return conflict("immutable transport bytes differ: %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ensureControllerPath(path); err != nil {
		return err
	}
	return AtomicWrite(path, append([]byte(nil), data...), false)
}

func validateNativeTransportIdentity(receipt map[string]any, engine EngineIdentity) error {
	if err := validateEngineIdentity(engine); err != nil {
		return blocked("native transport engine identity is invalid: %v", err)
	}
	executable, _ := receipt["executable"].(string)
	if !strings.EqualFold(filepath.Base(filepath.Clean(executable)), "pwsh.exe") {
		return validateNativeHostTransportIdentity(receipt, engine, executable)
	}
	argv, _ := nativeTransportStrings(receipt["argv"])
	if len(argv) != 7 || argv[0] != "-NoLogo" || argv[1] != "-NoProfile" ||
		argv[2] != "-NonInteractive" || argv[3] != "-ExecutionPolicy" ||
		argv[4] != "Bypass" || argv[5] != "-File" {
		return blocked("native transport PowerShell argv is not the fixed provider entrypoint")
	}
	// On a fresh run PolicyRoot is available from the trusted bundle. A
	// retained engine reconstructed from journal fields does not carry local
	// paths, so the script path in the host receipt is accepted only after its
	// bytes are hashed against the persisted provider identity.
	providerScript := argv[6]
	if engine.PolicyRoot != "" {
		expectedScript := filepath.Join(engine.PolicyRoot, "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")
		if !sameNativeTransportPath(providerScript, expectedScript) {
			return blocked("native transport PowerShell provider path differs from the trusted bundle")
		}
		providerScript = expectedScript
	}
	if err := verifyNativeTransportFile(providerScript, engine.ProviderSHA256, "provider script"); err != nil {
		return err
	}
	if engine.HostPath != "" {
		if err := verifyNativeTransportFile(engine.HostPath, engine.HostSHA256, "host executable"); err != nil {
			return err
		}
	}
	return nil
}

// validateNativeHostTransportIdentity accepts the native Go stage host
// receipt: the provider process is the same trusted host binary re-executed
// with the fixed `__provider` subcommand, so its bytes must hash exactly to
// the persisted host identity.
func validateNativeHostTransportIdentity(receipt map[string]any, engine EngineIdentity, executable string) error {
	argv, _ := nativeTransportStrings(receipt["argv"])
	if len(argv) != 1 || argv[0] != "__provider" {
		return blocked("native transport host argv is not the fixed provider subcommand")
	}
	if engine.HostPath != "" && !sameNativeTransportPath(executable, engine.HostPath) {
		return blocked("native transport host path differs from the engine binding")
	}
	if err := verifyNativeTransportFile(executable, engine.HostSHA256, "host executable"); err != nil {
		return err
	}
	return nil
}

func verifyNativeTransportFile(path, expectedHash, label string) error {
	resolved, err := SafePath(path)
	if err != nil {
		return blocked("trusted native %s path is unsafe: %v", label, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return blocked("trusted native %s is missing or not regular", label)
	}
	actual, err := hashNativeTransportFile(resolved)
	if err != nil {
		return blocked("trusted native %s could not be hashed: %v", label, err)
	}
	if actual != expectedHash {
		return blocked("trusted native %s hash differs from the engine binding", label)
	}
	return nil
}

func hashNativeTransportFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sameNativeTransportPath(left, right string) bool {
	leftPath, leftErr := SafePath(left)
	rightPath, rightErr := SafePath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftPath), filepath.Clean(rightPath))
}

func nativeTransportStrings(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		if typed == nil {
			return nil, false
		}
		result := append([]string(nil), typed...)
		for _, item := range result {
			if strings.IndexByte(item, 0) >= 0 {
				return nil, false
			}
		}
		return result, true
	case []any:
		if typed == nil {
			return nil, false
		}
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || strings.IndexByte(text, 0) >= 0 {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func nativeTransportInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float32:
		converted := float64(typed)
		if math.IsNaN(converted) || math.IsInf(converted, 0) || converted != math.Trunc(converted) || converted < math.MinInt64 || converted > math.MaxInt64 {
			return 0, false
		}
		return int64(converted), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}
