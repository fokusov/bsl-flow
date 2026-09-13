package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nativeTransportTestFixture struct {
	attempt     string
	engine      EngineIdentity
	observation ExecuteObservation
	transport   *ProviderTransportEvidence
}

func newNativeTransportTestFixture(t *testing.T) nativeTransportTestFixture {
	t.Helper()
	engineFixture := newNativeTestEngine(t)
	// The production runner resolves PowerShell 7 to an absolute pwsh.exe path.
	// A regular fixture file is enough to exercise the path/identity binding
	// without launching a process.
	pwsh := filepath.Join(engineFixture.root, "pwsh.exe")
	writeNativeTestFile(t, pwsh, []byte("PowerShell 7 fixture\n"))

	observation := ExecuteObservation{
		SchemaVersion: nativeControllerSchema,
		Contract:      NativeProviderContract,
		TaskID:        "11111111-2222-4333-8444-555555555555",
		AttemptID:     "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Stage:         "inspect",
		Status:        "completed",
		Summary:       "native transport fixture completed",
		Proposal:      map[string]any{"complexity": "S"},
		SideEffects:   "none",
		Dependencies:  map[string]any{"source": "source-hash"},
		SourceManifest: map[string]any{
			"schema_version": int64(1),
			"baseline":       "HEAD",
			"source_paths":   []any{"."},
			"files":          []any{},
			"sha256":         strings.Repeat("1", 64),
		},
		Artifacts: []ArtifactRef{},
		ProcessReceipt: map[string]any{
			"processes": []any{},
		},
		ProviderContract: providerContract(engineFixture.engine),
	}
	stdout, err := Canonical(toExecuteObservation(observation))
	if err != nil {
		t.Fatal(err)
	}
	stderr := []byte("native transport fixture stderr\n")
	providerScript := filepath.Join(engineFixture.engine.PolicyRoot, "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")
	receipt := map[string]any{
		"schema_version": float64(1),
		"executable":     pwsh,
		"argv":           []any{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", providerScript},
		"started":        true,
		"terminal":       true,
		"exit_code":      float64(0),
		"stop_reason":    "",
		"stdout_bytes":   float64(len(stdout)),
		"stderr_bytes":   float64(len(stderr)),
		"stdout_sha256":  fileSHA256(stdout),
		"stderr_sha256":  fileSHA256(stderr),
		"duration_ms":    float64(4),
	}
	attempt := filepath.Join(tempDir(t), "native-transport-attempt")
	if err := os.MkdirAll(attempt, 0o700); err != nil {
		t.Fatal(err)
	}
	return nativeTransportTestFixture{
		attempt:     attempt,
		engine:      engineFixture.engine,
		observation: observation,
		transport:   &ProviderTransportEvidence{Receipt: receipt, Stdout: stdout, Stderr: stderr},
	}
}

func TestNativeTransportRetainAndValidate(t *testing.T) {
	fixture := newNativeTransportTestFixture(t)
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{nativeTransportReceiptFile, nativeTransportStdoutFile, nativeTransportStderrFile} {
		if _, err := os.Stat(filepath.Join(fixture.attempt, name)); err != nil {
			t.Fatalf("retained transport file %s is missing: %v", name, err)
		}
	}
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err != nil {
		t.Fatalf("valid retained transport was rejected: %v", err)
	}
	// A retry with the same host evidence is idempotent.
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatalf("same transport retry was not idempotent: %v", err)
	}
}

func TestNativeTransportForgedRecordWithoutHostTransportIsBlocked(t *testing.T) {
	fixture := newNativeTransportTestFixture(t)
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err == nil {
		t.Fatal("record accepted an observation without the host transport receipt")
	} else if typed, ok := err.(*KindError); !ok || typed.Kind != "BF_BLOCKED" {
		t.Fatalf("missing host transport did not return BF_BLOCKED: %v", err)
	}
}

func TestNativeTransportObservationAndStreamDriftAreBlocked(t *testing.T) {
	fixture := newNativeTransportTestFixture(t)
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatal(err)
	}
	changed := fixture.observation
	changed.Summary = "forged provider observation"
	if err := validateNativeTransport(fixture.attempt, changed, fixture.engine); err == nil {
		t.Fatal("changed observation was accepted against retained stdout")
	}
	if err := os.WriteFile(filepath.Join(fixture.attempt, nativeTransportStdoutFile), []byte("forged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err == nil {
		t.Fatal("changed retained stdout was accepted")
	}
}

func TestNativeTransportReceiptAndTrustedInvocationDriftAreBlocked(t *testing.T) {
	fixture := newNativeTransportTestFixture(t)
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(fixture.attempt, nativeTransportReceiptFile)
	envelope, err := DecodeObject(mustReadNativeTransportTestFile(t, receiptPath))
	if err != nil {
		t.Fatal(err)
	}
	envelope["stdout_sha256"] = strings.Repeat("0", 64)
	data, err := Canonical(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err == nil {
		t.Fatal("receipt stream hash drift was accepted")
	}

	// Recreate the immutable fixture, then change only the fixed provider path
	// inside the host receipt. The bytes still hash correctly, so this exercises
	// trusted invocation pinning rather than stream integrity.
	fixture = newNativeTransportTestFixture(t)
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatal(err)
	}
	envelope, err = DecodeObject(mustReadNativeTransportTestFile(t, filepath.Join(fixture.attempt, nativeTransportReceiptFile)))
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := envelope["receipt"].(map[string]any)
	if !ok {
		t.Fatal("retained receipt is not an object")
	}
	argv, ok := receipt["argv"].([]any)
	if !ok || len(argv) != 7 {
		t.Fatalf("retained argv has unexpected shape: %#v", receipt["argv"])
	}
	argv[6] = filepath.Join(fixture.engine.PolicyRoot, "1c-task", "scripts", "different.ps1")
	receipt["argv"] = argv
	envelope["receipt"] = receipt
	data, err = Canonical(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.attempt, nativeTransportReceiptFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err == nil {
		t.Fatal("trusted provider path drift was accepted")
	}
}

func TestNativeTransportRetainsTerminalNonzeroExit(t *testing.T) {
	fixture := newNativeTransportTestFixture(t)
	fixture.transport.Receipt["exit_code"] = float64(7)
	fixture.transport.Receipt["stop_reason"] = "exit"
	if err := retainNativeTransport(fixture.attempt, fixture.transport); err != nil {
		t.Fatalf("terminal nonzero process receipt was not retained: %v", err)
	}
	if _, _, err := readNativeTransport(fixture.attempt); err != nil {
		t.Fatalf("retained terminal nonzero process receipt could not be read: %v", err)
	}
	if err := validateNativeTransport(fixture.attempt, fixture.observation, fixture.engine); err == nil {
		t.Fatal("failed process receipt was accepted as a successful provider observation")
	}
}

func mustReadNativeTransportTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
