package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTempDir mirrors t.TempDir with a retried cleanup: on Windows a freshly
// written file can still be held by an indexer when the framework's single
// RemoveAll pass runs, which fails the test after its assertions passed.
func newTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "bf-bootstrap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for attempt := 0; ; attempt++ {
			err := os.RemoveAll(root)
			if err == nil || attempt >= 20 {
				if err != nil {
					t.Logf("temp cleanup kept failing: %v", err)
				}
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	return root
}

const (
	testAgentsTemplate = "# Project instructions\n" +
		"\n" +
		"## Project\n" +
		"\n" +
		"- Client: <!-- fill when known -->\n" +
		"\n" +
		"<!-- bsl-flow managed:start -->\n" +
		"## BSL Flow task workflow\n" +
		"\n" +
		"- Use the installed 1c-task entrypoint for a registered task.\n" +
		"<!-- bsl-flow managed:end -->\n" +
		"\n" +
		"## Development rules\n" +
		"\n" +
		"- Keep changes minimal.\n"

	testConfigTemplate = "version: 2\n" +
		"\n" +
		"workflow:\n" +
		"  mode: assisted\n" +
		"  entrypoint: 1c-task\n" +
		"\n" +
		"source:\n" +
		"  paths:\n" +
		"    - src\n" +
		"\n" +
		"review:\n" +
		"  enabled: true\n" +
		"  max_review_fix_rounds: 1\n"

	testGitIgnoreTemplate = "# bsl-flow managed:start\n" +
		".bsl-flow/reports/*\n" +
		"!.bsl-flow/reports/.gitkeep\n" +
		".bsl-flow/tasks/\n" +
		"# bsl-flow managed:end\n"

	testSentinelTemplate = "format_version: 1\n" +
		"framework: bsl-flow\n" +
		"framework_version: \"0.8.0-dev.1\"\n" +
		"initialized_at: \"__INITIALIZED_AT__\"\n"
)

func staticTemplates(files map[string]string) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		content, ok := files[rel]
		if !ok {
			return nil, fmt.Errorf("template not found: %s", rel)
		}
		return []byte(content), nil
	}
}

func fullTemplates() func(string) ([]byte, error) {
	return staticTemplates(map[string]string{
		"AGENTS.md":              testAgentsTemplate,
		"bsl-flow.yaml":          testConfigTemplate,
		".gitignore":             testGitIgnoreTemplate,
		".bsl-flow/project.yaml": testSentinelTemplate,
	})
}

func writeProjectFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readProjectFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func applyProject(t *testing.T, root string, templates func(string) ([]byte, error)) Plan {
	t.Helper()
	plan, err := Inspect(root, templates)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := Apply(plan, templates); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return plan
}

func plannedFile(t *testing.T, plan Plan, rel string) FilePlan {
	t.Helper()
	for _, file := range plan.Files {
		if file.RelPath == rel {
			return file
		}
	}
	t.Fatalf("plan lacks managed file %s", rel)
	return FilePlan{}
}

func appliedFile(t *testing.T, applied []AppliedFile, rel string) AppliedFile {
	t.Helper()
	for _, result := range applied {
		if result.RelPath == rel {
			return result
		}
	}
	t.Fatalf("apply results lack managed file %s", rel)
	return AppliedFile{}
}

func assertNoBareLF(t *testing.T, text string) {
	t.Helper()
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && (i == 0 || text[i-1] != '\r') {
			t.Fatalf("bare LF at offset %d in %q", i, text)
		}
	}
}

func TestFreshProjectCreatesAllThenSkipsIdempotently(t *testing.T) {
	root := newTempDir(t)
	templates := fullTemplates()

	plan, err := Inspect(root, templates)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(plan.Files) != len(managedFiles) {
		t.Fatalf("plan has %d files, want %d", len(plan.Files), len(managedFiles))
	}
	for _, file := range plan.Files {
		if file.Action != ActionCreate {
			t.Errorf("%s: planned action = %q, want %q", file.RelPath, file.Action, ActionCreate)
		}
	}
	applied, err := Apply(plan, templates)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, result := range applied {
		if result.Action != ActionCreate {
			t.Errorf("%s: applied action = %q, want %q", result.RelPath, result.Action, ActionCreate)
		}
	}
	if got := readProjectFile(t, root, ".bsl-flow/project.yaml"); got != testSentinelTemplate {
		t.Errorf("sentinel = %q, want template bytes verbatim", got)
	}
	if ok, problems := Verify(root, templates); !ok {
		t.Fatalf("verify after apply failed: %v", problems)
	}

	first := map[string]string{}
	for _, managed := range managedFiles {
		first[managed.RelPath] = readProjectFile(t, root, managed.RelPath)
	}

	replan, err := Inspect(root, templates)
	if err != nil {
		t.Fatalf("re-inspect: %v", err)
	}
	for _, file := range replan.Files {
		if file.Action != ActionSkip {
			t.Errorf("%s: second-run action = %q, want %q (%s)", file.RelPath, file.Action, ActionSkip, file.Reason)
		}
	}
	reapplied, err := Apply(replan, templates)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	for _, result := range reapplied {
		if result.Action != ActionSkip {
			t.Errorf("%s: second-run applied action = %q, want %q", result.RelPath, result.Action, ActionSkip)
		}
	}
	for rel, content := range first {
		if got := readProjectFile(t, root, rel); got != content {
			t.Errorf("%s changed on idempotent re-run", rel)
		}
	}
}

func TestMergeYAMLMissingNestedKeyInsertsOnlyThatLine(t *testing.T) {
	root := newTempDir(t)
	existing := "version: 2\n" +
		"\n" +
		"# user comment about workflow\n" +
		"workflow:\n" +
		"  mode: assisted  # user choice\n" +
		"  entrypoint: 1c-task\n" +
		"\n" +
		"source:\n" +
		"  paths:\n" +
		"    - src\n" +
		"\n" +
		"review:\n" +
		"  # review disabled during migration\n" +
		"  max_review_fix_rounds: 1\n" +
		"user_tail: 1\n"
	writeProjectFile(t, root, "bsl-flow.yaml", existing)

	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, "bsl-flow.yaml"); file.Action != ActionMerge || !strings.Contains(file.Reason, "review.enabled") {
		t.Fatalf("bsl-flow.yaml plan = %+v, want merge mentioning review.enabled", file)
	}

	insert := "  enabled: true\n"
	at := strings.Index(existing, "user_tail: 1\n")
	expected := existing[:at] + insert + existing[at:]
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != expected {
		t.Fatalf("merged config:\n%q\nwant:\n%q", got, expected)
	}
	if !strings.HasPrefix(readProjectFile(t, root, "bsl-flow.yaml"), existing[:at]) {
		t.Fatal("original prefix bytes were modified")
	}
	if ok, problems := Verify(root, fullTemplates()); !ok {
		t.Fatalf("verify failed: %v", problems)
	}
}

func TestMergeYAMLMissingTopLevelKeyAppendsAtEnd(t *testing.T) {
	root := newTempDir(t)
	existing := "version: 2\n" +
		"\n" +
		"# user prefers assisted\n" +
		"workflow:\n" +
		"  mode: assisted\n" +
		"  entrypoint: 1c-task\n" +
		"\n" +
		"review:\n" +
		"  enabled: true\n" +
		"  max_review_fix_rounds: 1\n"
	writeProjectFile(t, root, "bsl-flow.yaml", existing)

	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, "bsl-flow.yaml"); file.Action != ActionMerge || !strings.Contains(file.Reason, "source") {
		t.Fatalf("bsl-flow.yaml plan = %+v, want merge mentioning source", file)
	}
	// The appended subtree keeps the template's trailing blank line, exactly
	// like Get-Subtree in Update-BSLFlowProject.ps1:93-103.
	expected := existing + "\nsource:\n  paths:\n    - src\n\n"
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != expected {
		t.Fatalf("merged config:\n%q\nwant:\n%q", got, expected)
	}
}

func TestMergeYAMLCompleteFileSkips(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, "bsl-flow.yaml", "# user header\n"+testConfigTemplate+"# user footer\n")
	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, "bsl-flow.yaml"); file.Action != ActionSkip {
		t.Fatalf("bsl-flow.yaml plan = %+v, want skip", file)
	}
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != "# user header\n"+testConfigTemplate+"# user footer\n" {
		t.Fatal("satisfied config was rewritten")
	}
}

func TestMergeGitIgnoreAppendsBlockPreservingUserLines(t *testing.T) {
	root := newTempDir(t)
	existing := "# user ignore rules\nnode_modules/\n*.log\n"
	writeProjectFile(t, root, ".gitignore", existing)

	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, ".gitignore"); file.Action != ActionMerge {
		t.Fatalf(".gitignore plan = %+v, want merge", file)
	}
	expected := existing + "\n# bsl-flow managed:start\n.bsl-flow/reports/*\n!.bsl-flow/reports/.gitkeep\n.bsl-flow/tasks/\n# bsl-flow managed:end\n"
	if got := readProjectFile(t, root, ".gitignore"); got != expected {
		t.Fatalf("merged .gitignore:\n%q\nwant:\n%q", got, expected)
	}
}

func TestMergeGitIgnoreReplacesOutdatedBlockInPlace(t *testing.T) {
	root := newTempDir(t)
	existing := "user line\n# bsl-flow managed:start\nold-managed-entry\n# bsl-flow managed:end\ntrailing user\n"
	writeProjectFile(t, root, ".gitignore", existing)

	applyProject(t, root, fullTemplates())
	expected := "user line\n# bsl-flow managed:start\n.bsl-flow/reports/*\n!.bsl-flow/reports/.gitkeep\n.bsl-flow/tasks/\n# bsl-flow managed:end\ntrailing user\n"
	if got := readProjectFile(t, root, ".gitignore"); got != expected {
		t.Fatalf("updated .gitignore:\n%q\nwant:\n%q", got, expected)
	}
}

func TestMergeGitIgnoreIncompleteBlockBlocks(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, ".gitignore", "user\n# bsl-flow managed:start\nno-end-marker\n")
	if _, err := Inspect(root, fullTemplates()); err == nil {
		t.Fatal("inspect accepted an incomplete managed block")
	}
}

func TestMergeAgentsAppendsOnlyManagedBlock(t *testing.T) {
	root := newTempDir(t)
	existing := "# My project\n\nMy own rules here.\n"
	writeProjectFile(t, root, "AGENTS.md", existing)

	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, "AGENTS.md"); file.Action != ActionMerge {
		t.Fatalf("AGENTS.md plan = %+v, want merge", file)
	}
	expected := existing + "\n<!-- bsl-flow managed:start -->\n## BSL Flow task workflow\n\n- Use the installed 1c-task entrypoint for a registered task.\n<!-- bsl-flow managed:end -->\n"
	got := readProjectFile(t, root, "AGENTS.md")
	if got != expected {
		t.Fatalf("merged AGENTS.md:\n%q\nwant:\n%q", got, expected)
	}
	if strings.Contains(got, "Development rules") || strings.Contains(got, "fill when known") {
		t.Fatal("template-only sections leaked into the existing AGENTS.md")
	}
}

func TestMergeAgentsReplacesOutdatedBlock(t *testing.T) {
	root := newTempDir(t)
	existing := "# My project\n\n<!-- bsl-flow managed:start -->\n- Outdated instruction.\n<!-- bsl-flow managed:end -->\n\nUser rule.\n"
	writeProjectFile(t, root, "AGENTS.md", existing)

	applyProject(t, root, fullTemplates())
	expected := "# My project\n\n<!-- bsl-flow managed:start -->\n## BSL Flow task workflow\n\n- Use the installed 1c-task entrypoint for a registered task.\n<!-- bsl-flow managed:end -->\n\nUser rule.\n"
	if got := readProjectFile(t, root, "AGENTS.md"); got != expected {
		t.Fatalf("updated AGENTS.md:\n%q\nwant:\n%q", got, expected)
	}
}

func TestCRLFFilesStayCRLFAfterMerge(t *testing.T) {
	tests := []struct {
		name     string
		rel      string
		existing string
		expected string
	}{
		{
			name: "gitignore append",
			rel:  ".gitignore",
			existing: "# user\r\n" +
				"node_modules/\r\n",
			expected: "# user\r\n" +
				"node_modules/\r\n" +
				"\r\n" +
				"# bsl-flow managed:start\r\n" +
				".bsl-flow/reports/*\r\n" +
				"!.bsl-flow/reports/.gitkeep\r\n" +
				".bsl-flow/tasks/\r\n" +
				"# bsl-flow managed:end\r\n",
		},
		{
			name: "yaml nested insert",
			rel:  "bsl-flow.yaml",
			existing: "version: 2\r\n" +
				"\r\n" +
				"workflow:\r\n" +
				"  mode: assisted\r\n" +
				"  entrypoint: 1c-task\r\n" +
				"\r\n" +
				"source:\r\n" +
				"  paths:\r\n" +
				"    - src\r\n" +
				"\r\n" +
				"review:\r\n" +
				"  max_review_fix_rounds: 1\r\n" +
				"user_tail: 1\r\n",
			expected: "version: 2\r\n" +
				"\r\n" +
				"workflow:\r\n" +
				"  mode: assisted\r\n" +
				"  entrypoint: 1c-task\r\n" +
				"\r\n" +
				"source:\r\n" +
				"  paths:\r\n" +
				"    - src\r\n" +
				"\r\n" +
				"review:\r\n" +
				"  max_review_fix_rounds: 1\r\n" +
				"  enabled: true\r\n" +
				"user_tail: 1\r\n",
		},
		{
			name: "agents append",
			rel:  "AGENTS.md",
			existing: "# My project\r\n" +
				"Rules.\r\n",
			expected: "# My project\r\n" +
				"Rules.\r\n" +
				"\r\n" +
				"<!-- bsl-flow managed:start -->\r\n" +
				"## BSL Flow task workflow\r\n" +
				"\r\n" +
				"- Use the installed 1c-task entrypoint for a registered task.\r\n" +
				"<!-- bsl-flow managed:end -->\r\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTempDir(t)
			writeProjectFile(t, root, test.rel, test.existing)
			applyProject(t, root, fullTemplates())
			got := readProjectFile(t, root, test.rel)
			if got != test.expected {
				t.Fatalf("merged file:\n%q\nwant:\n%q", got, test.expected)
			}
			assertNoBareLF(t, got)
		})
	}
}

func TestMixedEndingsKeepOriginalBytesAndUseDominantTerminator(t *testing.T) {
	root := newTempDir(t)
	existing := "version: 2\r\n" +
		"\n" +
		"workflow:\r\n" +
		"  mode: assisted\r\n" +
		"\r\n" +
		"source:\n" +
		"  paths:\n" +
		"    - src\n" +
		"\n" +
		"review:\r\n" +
		"  enabled: true\r\n" +
		"  max_review_fix_rounds: 1\r\n"
	writeProjectFile(t, root, "bsl-flow.yaml", existing)

	applyProject(t, root, fullTemplates())
	got := readProjectFile(t, root, "bsl-flow.yaml")
	at := strings.Index(existing, "source:")
	expected := existing[:at] + "  entrypoint: 1c-task\r\n" + existing[at:]
	if got != expected {
		t.Fatalf("merged config:\n%q\nwant:\n%q", got, expected)
	}
}

func TestApplyRejectsPathTraversal(t *testing.T) {
	root := newTempDir(t)
	evil := []string{"../evil.txt", "..\\evil.txt", "/etc/passwd", "C:\\Windows\\evil.txt", "a/../../evil.txt"}
	for _, rel := range evil {
		plan := Plan{ProjectRoot: root, Files: []FilePlan{{RelPath: rel, Action: ActionCreate}}}
		if _, err := Apply(plan, fullTemplates()); err == nil {
			t.Errorf("apply accepted traversal path %q", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "evil.txt")); !os.IsNotExist(err) {
		t.Error("traversal attempt wrote outside the project root")
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"AGENTS.md", ".gitignore", ".bsl-flow/project.yaml", ".bsl-flow/reports/.gitkeep", "dir/sub/file.txt"}
	for _, rel := range valid {
		if err := validateRelPath(rel); err != nil {
			t.Errorf("validateRelPath(%q) = %v, want nil", rel, err)
		}
	}
	invalid := []string{"", "..", ".", "../evil", "..\\evil", "/etc/passwd", "\\windows", "C:\\evil", "C:evil", "a/../../b", "a//b", "./x", "a/.", "trailing/"}
	for _, rel := range invalid {
		if err := validateRelPath(rel); err == nil {
			t.Errorf("validateRelPath(%q) = nil, want error", rel)
		}
	}
}

func TestApplyRejectsUnmanagedRelPathAndUnknownAction(t *testing.T) {
	root := newTempDir(t)
	if _, err := Apply(Plan{ProjectRoot: root, Files: []FilePlan{{RelPath: "notes.txt", Action: ActionCreate}}}, fullTemplates()); err == nil {
		t.Error("apply accepted an unmanaged relative path")
	}
	if _, err := Apply(Plan{ProjectRoot: root, Files: []FilePlan{{RelPath: ".gitignore", Action: "destroy"}}}, fullTemplates()); err == nil {
		t.Error("apply accepted an unknown action")
	}
}

func TestMissingTemplateReturnsTypedErrorAndWritesNothing(t *testing.T) {
	tests := []struct {
		name      string
		templates func(string) ([]byte, error)
	}{
		{
			name: "template source fails",
			templates: func(rel string) ([]byte, error) {
				return nil, os.ErrNotExist
			},
		},
		{
			name: "template empty",
			templates: func(rel string) ([]byte, error) {
				return []byte{}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTempDir(t)
			existing := "version: 2\nworkflow:\n  mode: assisted\n  entrypoint: 1c-task\nsource:\n  paths:\n    - src\nreview:\n  max_review_fix_rounds: 1\n"
			writeProjectFile(t, root, "bsl-flow.yaml", existing)
			_, err := Inspect(root, test.templates)
			var missing *MissingTemplateError
			if !errors.As(err, &missing) {
				t.Fatalf("inspect error = %v, want MissingTemplateError", err)
			}
			if missing.RelPath != "bsl-flow.yaml" {
				t.Errorf("missing template path = %q, want bsl-flow.yaml", missing.RelPath)
			}

			fresh := newTempDir(t)
			plan, planErr := Inspect(fresh, fullTemplates())
			if planErr != nil {
				t.Fatalf("inspect with full templates: %v", planErr)
			}
			if _, err := Apply(plan, test.templates); !errors.As(err, &missing) {
				t.Fatalf("apply error = %v, want MissingTemplateError", err)
			}
			for _, managed := range managedFiles {
				if _, statErr := os.Stat(filepath.Join(fresh, filepath.FromSlash(managed.RelPath))); !os.IsNotExist(statErr) {
					t.Errorf("%s exists after a failed apply", managed.RelPath)
				}
			}
		})
	}
}

func TestInspectRejectsDirectoryInPlaceOfManagedFile(t *testing.T) {
	root := newTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".gitignore"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, fullTemplates()); err == nil || !strings.Contains(err.Error(), "directory exists where a managed file is required") {
		t.Fatalf("inspect error = %v, want managed-directory rejection", err)
	}
}

func TestInspectRejectsUnsupportedUserYAML(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, "bsl-flow.yaml", "workflow:\n\tmode: assisted\n")
	if _, err := Inspect(root, fullTemplates()); err == nil || !strings.Contains(err.Error(), "tab indentation") {
		t.Fatalf("inspect error = %v, want tab-indentation rejection", err)
	}
}

func TestSentinelAndPlaceholdersAreNeverMerged(t *testing.T) {
	root := newTempDir(t)
	sentinel := "format_version: 1\nframework: bsl-flow\nframework_version: \"0.1.0\"\n# user note\n"
	writeProjectFile(t, root, ".bsl-flow/project.yaml", sentinel)
	writeProjectFile(t, root, ".bsl-flow/reports/.gitkeep", "user data")

	plan := applyProject(t, root, fullTemplates())
	if file := plannedFile(t, plan, ".bsl-flow/project.yaml"); file.Action != ActionSkip {
		t.Errorf("sentinel plan = %+v, want skip", file)
	}
	if file := plannedFile(t, plan, ".bsl-flow/reports/.gitkeep"); file.Action != ActionSkip {
		t.Errorf("placeholder plan = %+v, want skip", file)
	}
	if got := readProjectFile(t, root, ".bsl-flow/project.yaml"); got != sentinel {
		t.Fatalf("sentinel rewritten:\n%q\nwant:\n%q", got, sentinel)
	}
	if got := readProjectFile(t, root, ".bsl-flow/reports/.gitkeep"); got != "user data" {
		t.Fatalf("placeholder rewritten: %q", got)
	}
}

func TestVerifyReportsPendingActions(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, ".gitignore", "user rules only\n")
	ok, problems := Verify(root, fullTemplates())
	if ok {
		t.Fatal("verify passed with pending actions")
	}
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, ".gitignore requires merge") {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems %v lack the .gitignore merge entry", problems)
	}
}

func TestApplyCreatesWhenPlannedMergeFileDisappeared(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, "bsl-flow.yaml", "version: 2\nworkflow:\n  mode: assisted\n  entrypoint: 1c-task\nreview:\n  max_review_fix_rounds: 1\n")
	plan, err := Inspect(root, fullTemplates())
	if err != nil {
		t.Fatal(err)
	}
	if file := plannedFile(t, plan, "bsl-flow.yaml"); file.Action != ActionMerge {
		t.Fatalf("bsl-flow.yaml plan = %+v, want merge", file)
	}
	if err := os.Remove(filepath.Join(root, "bsl-flow.yaml")); err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(plan, fullTemplates())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result := appliedFile(t, applied, "bsl-flow.yaml"); result.Action != ActionCreate {
		t.Errorf("bsl-flow.yaml applied action = %q, want create", result.Action)
	}
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != testConfigTemplate {
		t.Fatalf("recreated config = %q, want template bytes", got)
	}
}

func TestApplyPreservesFileAppearingAfterInspect(t *testing.T) {
	root := newTempDir(t)
	plan, err := Inspect(root, fullTemplates())
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	userAgents := "# Owned by the user\n"
	writeProjectFile(t, root, "AGENTS.md", userAgents)
	applied, err := Apply(plan, fullTemplates())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result := appliedFile(t, applied, "AGENTS.md"); result.Action != ActionSkip {
		t.Errorf("AGENTS.md applied action = %q, want skip", result.Action)
	}
	if got := readProjectFile(t, root, "AGENTS.md"); got != userAgents {
		t.Fatalf("AGENTS.md rewritten despite create-preservation: %q", got)
	}
}

func TestAgentsTemplateWithoutManagedBlockIsRejected(t *testing.T) {
	root := newTempDir(t)
	writeProjectFile(t, root, "AGENTS.md", "# project\n")
	templates := staticTemplates(map[string]string{
		"AGENTS.md":              "# Template without markers\n",
		"bsl-flow.yaml":          testConfigTemplate,
		".gitignore":             testGitIgnoreTemplate,
		".bsl-flow/project.yaml": testSentinelTemplate,
	})
	if _, err := Inspect(root, templates); err == nil || !strings.Contains(err.Error(), "managed block") {
		t.Fatalf("inspect error = %v, want managed-block rejection", err)
	}
}
