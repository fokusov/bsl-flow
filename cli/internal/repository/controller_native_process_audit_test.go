package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeLifecycleProcessAuditHasNoPowerShell is the Windows-scope
// requirement-22 process audit: it drives the deterministic S lifecycle to
// acceptance with the existing native fixture and then walks every attempt
// and artifact directory the run produced, asserting that no process receipt
// the engine persisted names or references PowerShell.
//
// Audit boundary (important): the fake provider's observation carries a
// fixture-fabricated outer transport receipt (nativeTestTransport) that
// deliberately names pwsh.exe. That receipt is test INPUT handed to the
// engine, not a process the engine launched, so its executable is not
// treated as a lifecycle violation here. Everything this audit walks is
// what the engine itself wrote during the run: the provider-execution
// process/exit receipts under the artifact root, their canonical copies
// under attempts/<id>/artifacts, and the transport envelopes persisted
// beside each attempt by persistTransportEvidence. The engine never
// launches the transport it is handed; it only validates and retains it.
//
// A real (non-fixture) run writes host-shaped transport receipts — the same
// trusted binary re-executed with the fixed `__provider` subcommand — and
// every persisted transport receipt is therefore additionally required to
// satisfy validateNativeTransportIdentity, the exact production consumer of
// the nativeTransportReceiptFields schema: a receipt must bind either to
// the host binary identity (path + hash + fixed argv) or to the retained
// legacy provider entrypoint bound to the persisted provider script hash.
func TestNativeLifecycleProcessAuditHasNoPowerShell(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	nativeTestRunUntilAccept(t, fixture)
	if _, err := commandControllerAccept(fixture.project, fixture.taskID); err != nil {
		t.Fatalf("accept native fixture: %v", err)
	}
	repository, err := OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, _ := nativeTestTaskPayload(t, fixture)
	attempts := anyItems(payload["attempts"])
	if len(attempts) == 0 {
		t.Fatalf("accepted lifecycle retained no attempts: %#v", payload["attempts"])
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The fake provider executable is the request's trusted execution_profile
	// executable, which this fixture pins to the fixture host binary.
	providerExecutable := asStringOr(asMap(asMap(payload["request"])["execution_profile"])["executable"])
	audit := nativeProcessAudit{
		engine:        fixture.engine.engine,
		testBinary:    testBinary,
		providerExec:  providerExecutable,
		attemptsRoot:  filepath.Join(repository.StorePath, "tasks", fixture.taskID, "attempts"),
		providerRoots: filepath.Join(repository.Worktree, ".bsl-flow", "hosts", "native", fixture.taskID),
	}
	if err := audit.walk(audit.attemptsRoot); err != nil {
		t.Fatalf("attempt directory audit failed: %v", err)
	}
	if err := audit.walk(audit.providerRoots); err != nil {
		t.Fatalf("artifact directory audit failed: %v", err)
	}
	if audit.processReceipts == 0 {
		t.Fatal("audit found no process.json receipts; coverage is vacuous")
	}
	if audit.transportReceipts == 0 {
		t.Fatal("audit found no transport.json receipts; coverage is vacuous")
	}
	if audit.jsonDocuments == 0 {
		t.Fatal("audit found no JSON documents to scan for argv; coverage is vacuous")
	}
	t.Logf("audited %d attempts: %d process.json + %d exit.json receipts, %d transport receipts, %d JSON documents argv-scanned",
		len(attempts), audit.processReceipts, audit.exitReceipts, audit.transportReceipts, audit.jsonDocuments)
}

// nativeProcessAudit accumulates the receipt families the audit walked.
type nativeProcessAudit struct {
	engine            EngineIdentity
	testBinary        string
	providerExec      string
	attemptsRoot      string
	providerRoots     string
	processReceipts   int
	exitReceipts      int
	transportReceipts int
	jsonDocuments     int
}

// walk visits one rooted tree of the run and audits every persisted file.
func (a *nativeProcessAudit) walk(root string) error {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}
	return native1CWalkFiles(root, func(path string) error {
		name := filepath.Base(path)
		switch {
		case strings.EqualFold(name, "transport.json"):
			a.transportReceipts++
			return a.auditTransportReceipt(path)
		case strings.EqualFold(name, "process.json"):
			a.processReceipts++
			return a.auditProcessReceipt(path)
		case strings.EqualFold(name, "exit.json"):
			a.exitReceipts++
			return a.auditProcessReceipt(path)
		case strings.EqualFold(filepath.Ext(name), ".json"):
			a.jsonDocuments++
			return auditNativeJSONArgvTokens(path)
		}
		return nil
	})
}

// auditProcessReceipt checks one engine-persisted process/exit receipt: the
// recorded executable must be a trusted launcher (the host binary, the test
// binary, git, or the pinned provider executable) and never PowerShell.
func (a *nativeProcessAudit) auditProcessReceipt(path string) error {
	document, err := native1CReadJSON(path)
	if err != nil {
		return err
	}
	executable := asStringOr(document["executable"])
	if err := assertNoNativeAuditPowerShellToken(executable); err != nil {
		return err
	}
	switch {
	case sameNativeTransportPath(executable, a.engine.HostPath),
		sameNativeTransportPath(executable, a.testBinary),
		sameNativeTransportPath(executable, a.providerExec),
		nativeAuditIsGit(executable):
		return auditNativeJSONArgvTokens(path)
	default:
		return blocked("process receipt %s names untrusted executable %q", path, executable)
	}
}

// auditTransportReceipt consumes a persisted transport envelope exactly the
// way production does: the receipt must carry exactly the
// nativeTransportReceiptFields schema and must bind to a trusted identity.
func (a *nativeProcessAudit) auditTransportReceipt(path string) error {
	envelope, err := native1CReadJSON(path)
	if err != nil {
		return err
	}
	receipt, ok := envelope["receipt"].(map[string]any)
	if !ok || receipt == nil {
		return blocked("transport envelope %s has no receipt object", path)
	}
	if len(receipt) != len(nativeTransportReceiptFields) {
		return blocked("transport receipt %s does not match the nativeTransportReceiptFields schema", path)
	}
	for field := range receipt {
		if !nativeTransportReceiptFields[field] {
			return blocked("transport receipt %s carries unsupported field %s", path, field)
		}
	}
	if err := validateNativeTransportIdentity(receipt, a.engine); err != nil {
		return err
	}
	argv, _ := nativeTransportStrings(receipt["argv"])
	for _, argument := range argv {
		if err := assertNoNativeAuditPowerShellToken(argument); err != nil {
			return err
		}
	}
	return nil
}

// auditNativeJSONArgvTokens scans any recorded argv array inside a JSON
// document; the managed runner records only arguments_sha256, so a raw argv
// may only ever appear in host transport receipts and must stay
// PowerShell-free.
func auditNativeJSONArgvTokens(path string) error {
	document, err := native1CReadJSON(path)
	if err != nil {
		// Only canonical JSON documents participate in the argv scan.
		return nil
	}
	return nativeAuditScanArgv(document, assertNoNativeAuditPowerShellToken)
}

func assertNoNativeAuditPowerShellToken(value string) error {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "pwsh") || strings.Contains(lower, "powershell") {
		return blocked("recorded value %q references PowerShell", value)
	}
	return nil
}

// nativeAuditScanArgv walks a decoded JSON value and applies visit to every
// element of every argv array it finds.
func nativeAuditScanArgv(value any, visit func(string) error) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "argv" {
				if arguments, ok := nativeTransportStrings(item); ok {
					for _, argument := range arguments {
						if err := visit(argument); err != nil {
							return err
						}
					}
					continue
				}
			}
			if err := nativeAuditScanArgv(item, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := nativeAuditScanArgv(item, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeAuditIsGit(executable string) bool {
	base := strings.ToLower(filepath.Base(filepath.Clean(executable)))
	return base == "git" || base == "git.exe"
}
