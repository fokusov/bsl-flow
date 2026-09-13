package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchCanonicalTargetErrorsDoNotFallBack(t *testing.T) {
	project := newRepo(t)
	id := "11111111-2222-4333-8444-555555555555"

	// The target directory itself reserves the UUID, even when its journal is
	// orphaned.  A checkout-local copy must not become an alternate owner.
	canonical := filepath.Join(project, ".git", "bsl-flow", "tasks", id)
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(project, ".bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "000001.json"), []byte(`{"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	handled, code := DispatchWithHost([]string{"task", "status", "--project", project, "--task", id}, &stdout, &stderr, &ControllerHost{})
	if !handled || code != 11 {
		t.Fatalf("canonical orphan was allowed to fall back: handled=%v code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "BF_BLOCKED") {
		t.Fatalf("canonical error did not use controller envelope: %q", stdout.String())
	}
}

func TestDispatchOpenRepositoryErrorDoesNotFallBack(t *testing.T) {
	project := tempDir(t)
	id := "22222222-3333-4444-8555-666666666666"
	var stdout, stderr bytes.Buffer
	handled, code := DispatchWithHost([]string{"task", "status", "--project", project, "--task", id}, &stdout, &stderr, &ControllerHost{})
	if !handled || code != 2 {
		t.Fatalf("unverified repository was allowed to fall back: handled=%v code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "BF_INVALID") {
		t.Fatalf("repository resolution error did not use controller envelope: %q", stdout.String())
	}
}

func TestParseControllerActionOptionsIsClosedPerAction(t *testing.T) {
	project := `C:\project`
	id := "33333333-4444-4555-8666-777777777777"
	valid := []string{"--project", project, "--task", id}

	if _, err := parseControllerActionOptions("status", append([]string{}, valid...)); err != nil {
		t.Fatalf("valid status options rejected: %v", err)
	}
	if _, err := parseControllerActionOptions("status", append(append([]string{}, valid...), "--input", "request.json")); err == nil {
		t.Fatal("status accepted an inapplicable input option")
	}
	if _, err := parseControllerActionOptions("status", append(append([]string{}, valid...), "--task", id)); err == nil {
		t.Fatal("status accepted a repeated task option")
	}
	if _, err := parseControllerActionOptions("status", append(append([]string{}, valid...), "--unexpected")); err == nil {
		t.Fatal("status accepted an unknown option")
	}
	if _, err := parseControllerActionOptions("run", append(append([]string{}, valid...), "--engine", "legacy-powershell")); err == nil || !strings.Contains(err.Error(), "legacy engine") {
		t.Fatalf("legacy engine was not blocked on canonical task: %v", err)
	}
	if _, err := parseControllerActionOptions("run", append(append([]string{}, valid...), "--engine", "native")); err != nil {
		t.Fatalf("explicit native engine was rejected: %v", err)
	}
	if _, err := parseControllerActionOptions("rebind", append([]string{}, valid...)); err == nil {
		t.Fatal("rebind accepted missing expected revision and input")
	}
	if _, err := parseControllerActionOptions("rebind", append(append([]string{}, valid...), "--expected-revision", "1", "--input", "request.json")); err != nil {
		t.Fatalf("valid rebind options rejected: %v", err)
	}
}
