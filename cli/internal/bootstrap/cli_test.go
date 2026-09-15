package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliTestDeps(frameworkVersion string) cliDeps {
	return cliDeps{templates: fullTemplates(), frameworkVersion: frameworkVersion}
}

func runCLI(t *testing.T, deps cliDeps, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runCommand(args, deps, &out, &errOut)
	return code, out.String(), errOut.String()
}

func snapshotManagedFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string, len(managedFiles))
	for _, managed := range managedFiles {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(managed.RelPath)))
		if err == nil {
			snapshot[managed.RelPath] = string(data)
		}
	}
	return snapshot
}

const upgradeTestConfig = "version: 2\n" +
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

const upgradeTestAgents = "# My project\n\n<!-- bsl-flow managed:start -->\n- Outdated instruction.\n<!-- bsl-flow managed:end -->\n\nUser rule.\n"

const upgradeTestSentinel = "format_version: 1\nframework: bsl-flow\nframework_version: \"0.1.0\"\ninitialized_at: \"2020-01-01T00:00:00.0000000Z\"\n"

func writeUpgradeTargetProject(t *testing.T, root string) {
	t.Helper()
	writeProjectFile(t, root, "bsl-flow.yaml", upgradeTestConfig)
	writeProjectFile(t, root, "AGENTS.md", upgradeTestAgents)
	writeProjectFile(t, root, ".gitignore", "user rules only\n")
	writeProjectFile(t, root, ".bsl-flow/project.yaml", upgradeTestSentinel)
}

func TestCommandInitFreshProjectCreatesAndUpgradesManagedFiles(t *testing.T) {
	root := newTempDir(t)
	code, out, errOut := runCLI(t, cliTestDeps("0.9.0"), "init", "--project", root, "--explicit-1c-project")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
	for _, managed := range managedFiles {
		if !fileExists(filepath.Join(root, filepath.FromSlash(managed.RelPath))) {
			t.Errorf("%s was not created", managed.RelPath)
		}
	}
	sentinel := readProjectFile(t, root, ".bsl-flow/project.yaml")
	if strings.Contains(sentinel, "__INITIALIZED_AT__") {
		t.Errorf("sentinel kept the placeholder:\n%s", sentinel)
	}
	if !strings.Contains(sentinel, "framework_version: \"0.9.0\"") {
		t.Errorf("sentinel framework_version was not bumped to the installed version:\n%s", sentinel)
	}
	for _, expected := range []string{
		"BSL Flow project bootstrap complete: ",
		"Created: 6",
		"  + AGENTS.md",
		"  + bsl-flow.yaml",
		"  + .gitignore",
		"  + .bsl-flow/project.yaml",
		"  + .bsl-flow/reports/.gitkeep",
		"  + .bsl-flow/evidence/.gitkeep",
		"Preserved: 2",
		`"status": "applied"`,
		`"action": "update_framework_version"`,
	} {
		if !strings.Contains(out, expected) {
			t.Errorf("init output lacks %q; got:\n%s", expected, out)
		}
	}
	if ok, problems := Verify(root, fullTemplates()); !ok {
		t.Fatalf("verify after init failed: %v", problems)
	}
}

func TestCommandInitSecondRunChangesNothing(t *testing.T) {
	root := newTempDir(t)
	deps := cliTestDeps("0.9.0")
	if code, out, errOut := runCLI(t, deps, "init", "--project", root, "--explicit-1c-project"); code != 0 {
		t.Fatalf("first init exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	before := snapshotManagedFiles(t, root)

	code, out, errOut := runCLI(t, deps, "init", "--project", root, "--explicit-1c-project")
	if code != 0 {
		t.Fatalf("second init exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	if !strings.Contains(out, "Created: 0") {
		t.Errorf("second run planned creations; got:\n%s", out)
	}
	for rel, content := range before {
		if got := readProjectFile(t, root, rel); got != content {
			t.Errorf("%s changed on the idempotent re-run", rel)
		}
	}
}

func TestCommandUpgradeAppliesCommentPreservingMerge(t *testing.T) {
	root := newTempDir(t)
	writeUpgradeTargetProject(t, root)

	code, out, errOut := runCLI(t, cliTestDeps("0.2.0"), "upgrade", "--project", root, "--apply")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	for _, expected := range []string{
		`"action": "add_managed_path"`,
		`"path": "review.enabled"`,
		`"action": "update_managed_gitignore"`,
		`"action": "update_managed_agents"`,
		`"action": "update_framework_version"`,
		`"from": "0.1.0"`,
		`"to": "0.2.0"`,
		`"status": "changes_planned"`,
		`"status": "applied"`,
	} {
		if !strings.Contains(out, expected) {
			t.Errorf("upgrade output lacks %q; got:\n%s", expected, out)
		}
	}

	// Byte-exact comment-preserving results, matching the domain merge tests.
	at := strings.Index(upgradeTestConfig, "user_tail: 1\n")
	wantConfig := upgradeTestConfig[:at] + "  enabled: true\n" + upgradeTestConfig[at:]
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != wantConfig {
		t.Fatalf("upgraded config:\n%q\nwant:\n%q", got, wantConfig)
	}
	wantAgents := "# My project\n\n<!-- bsl-flow managed:start -->\n## BSL Flow task workflow\n\n- Use the installed 1c-task entrypoint for a registered task.\n<!-- bsl-flow managed:end -->\n\nUser rule.\n"
	if got := readProjectFile(t, root, "AGENTS.md"); got != wantAgents {
		t.Fatalf("upgraded AGENTS.md:\n%q\nwant:\n%q", got, wantAgents)
	}
	wantGitIgnore := "user rules only\n\n# bsl-flow managed:start\n.bsl-flow/reports/*\n!.bsl-flow/reports/.gitkeep\n.bsl-flow/tasks/\n# bsl-flow managed:end\n"
	if got := readProjectFile(t, root, ".gitignore"); got != wantGitIgnore {
		t.Fatalf("upgraded .gitignore:\n%q\nwant:\n%q", got, wantGitIgnore)
	}
	wantSentinel := strings.Replace(upgradeTestSentinel, `framework_version: "0.1.0"`, `framework_version: "0.2.0"`, 1)
	if got := readProjectFile(t, root, ".bsl-flow/project.yaml"); got != wantSentinel {
		t.Fatalf("upgraded sentinel:\n%q\nwant:\n%q", got, wantSentinel)
	}

	code, out, errOut = runCLI(t, cliTestDeps("0.2.0"), "upgrade", "--project", root)
	if code != 0 {
		t.Fatalf("re-upgrade exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	if !strings.Contains(out, `"status": "up_to_date"`) {
		t.Errorf("re-upgrade is not up_to_date; got:\n%s", out)
	}
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != wantConfig {
		t.Error("re-upgrade rewrote bsl-flow.yaml")
	}
}

func TestCommandUpgradeWithoutApplyWritesNothing(t *testing.T) {
	root := newTempDir(t)
	writeUpgradeTargetProject(t, root)
	before := snapshotManagedFiles(t, root)

	code, out, errOut := runCLI(t, cliTestDeps("0.2.0"), "upgrade", "--project", root)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	if !strings.Contains(out, `"apply_requested": false`) || !strings.Contains(out, `"status": "changes_planned"`) {
		t.Errorf("plan-only output is wrong; got:\n%s", out)
	}
	if strings.Contains(out, `"applied"`) {
		t.Errorf("plan-only run reported an apply; got:\n%s", out)
	}
	for rel, content := range before {
		if got := readProjectFile(t, root, rel); got != content {
			t.Errorf("%s changed on a plan-only run", rel)
		}
	}
}

func TestCommandUpgradePlanPathWritesPlanDocument(t *testing.T) {
	root := newTempDir(t)
	writeUpgradeTargetProject(t, root)
	planPath := filepath.Join(root, "artifacts", "plan.json")

	code, _, errOut := runCLI(t, cliTestDeps("0.2.0"), "upgrade", "--project", root, "--plan-path", planPath)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("plan file was not written: %v", err)
	}
	wantNewline := "\n"
	if os.PathSeparator == '\\' {
		wantNewline = "\r\n"
	}
	if !bytes.HasSuffix(data, []byte(wantNewline)) {
		t.Errorf("plan file must end with the platform newline: %q", data)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("plan file is not JSON: %v", err)
	}
	if document["schema_version"] != float64(1) || document["status"] != "changes_planned" {
		t.Errorf("plan document = %v", document)
	}
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != upgradeTestConfig {
		t.Error("plan-path run modified bsl-flow.yaml")
	}
}

func TestCommandInitRefusalPaths(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(t *testing.T, root string) []string
		wantMessage string
	}{
		{
			name: "unconfirmed project root",
			prepare: func(t *testing.T, root string) []string {
				return []string{"init", "--project", root}
			},
			wantMessage: "The directory is not confirmed as a 1C project root",
		},
		{
			name: "different openspec schema",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "openspec/config.yaml", "schema: other\n")
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "OpenSpec is already configured with schema 'other'",
		},
		{
			name: "duplicate openspec schema keys",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "openspec/config.yaml", "schema: one\nschema: two\n")
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "OpenSpec config contains duplicate schema keys",
		},
		{
			name: "nested parent git repository",
			prepare: func(t *testing.T, root string) []string {
				if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				project := filepath.Join(root, "project")
				if err := os.MkdirAll(project, 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{"init", "--project", project, "--explicit-1c-project"}
			},
			wantMessage: "The selected directory is inside another Git repository",
		},
		{
			name: "foreign sentinel framework",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, ".bsl-flow/project.yaml", "framework: other-tool\n")
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "The existing project sentinel belongs to another framework",
		},
		{
			name: "incompatible edit in bsl-flow.yaml",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "bsl-flow.yaml", "workflow:\n\tmode: assisted\n")
				writeProjectFile(t, root, ".bsl-flow/project.yaml", upgradeTestSentinel)
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "tab indentation",
		},
		{
			name: "duplicate managed gitignore block",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, ".gitignore", "# bsl-flow managed:start\na\n# bsl-flow managed:end\n# bsl-flow managed:start\nb\n# bsl-flow managed:end\n")
				writeProjectFile(t, root, "bsl-flow.yaml", upgradeTestConfig)
				writeProjectFile(t, root, ".bsl-flow/project.yaml", upgradeTestSentinel)
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "incomplete or duplicate BSL Flow managed block",
		},
		{
			name: "directory in place of managed file",
			prepare: func(t *testing.T, root string) []string {
				if err := os.MkdirAll(filepath.Join(root, ".gitignore"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{"init", "--project", root, "--explicit-1c-project"}
			},
			wantMessage: "A directory exists where a managed file is required",
		},
		{
			name: "filesystem root",
			prepare: func(t *testing.T, root string) []string {
				volume := filepath.VolumeName(root)
				if volume == "" {
					volume = string(filepath.Separator)
				}
				return []string{"init", "--project", volume + string(filepath.Separator)}
			},
			wantMessage: "refusing to bootstrap a filesystem root",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTempDir(t)
			args := test.prepare(t, root)
			code, _, errOut := runCLI(t, cliTestDeps("0.2.0"), args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr: %s", code, errOut)
			}
			if !strings.HasPrefix(errOut, "BF_BLOCKED: ") || !strings.Contains(errOut, test.wantMessage) {
				t.Fatalf("stderr = %q, want BF_BLOCKED containing %q", errOut, test.wantMessage)
			}
			if fileExists(filepath.Join(root, "AGENTS.md")) {
				t.Error("refused init created AGENTS.md")
			}
		})
	}
}

func TestCommandUpgradeRefusalPaths(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(t *testing.T, root string) []string
		wantMessage string
	}{
		{
			name: "newer project version",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "bsl-flow.yaml", upgradeTestConfig)
				writeProjectFile(t, root, ".bsl-flow/project.yaml", strings.Replace(upgradeTestSentinel, `"0.1.0"`, `"9.9.9"`, 1))
				return []string{"upgrade", "--project", root, "--apply"}
			},
			wantMessage: "Project version 9.9.9 is newer than installed framework 0.2.0",
		},
		{
			name: "missing project configuration",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, ".bsl-flow/project.yaml", upgradeTestSentinel)
				return []string{"upgrade", "--project", root, "--apply"}
			},
			wantMessage: "Missing project configuration",
		},
		{
			name: "missing project sentinel",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "bsl-flow.yaml", upgradeTestConfig)
				return []string{"upgrade", "--project", root, "--apply"}
			},
			wantMessage: "Missing project sentinel",
		},
		{
			name: "foreign sentinel framework",
			prepare: func(t *testing.T, root string) []string {
				writeProjectFile(t, root, "bsl-flow.yaml", upgradeTestConfig)
				writeProjectFile(t, root, ".bsl-flow/project.yaml", "framework: other-tool\n")
				return []string{"upgrade", "--project", root, "--apply"}
			},
			wantMessage: "Project sentinel is not owned by bsl-flow",
		},
		{
			name: "relative project path",
			prepare: func(t *testing.T, root string) []string {
				return []string{"upgrade", "--project", "relative/path", "--apply"}
			},
			wantMessage: "ProjectPath must be an absolute filesystem path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTempDir(t)
			args := test.prepare(t, root)
			code, _, errOut := runCLI(t, cliTestDeps("0.2.0"), args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr: %s", code, errOut)
			}
			if !strings.HasPrefix(errOut, "BF_BLOCKED: ") || !strings.Contains(errOut, test.wantMessage) {
				t.Fatalf("stderr = %q, want BF_BLOCKED containing %q", errOut, test.wantMessage)
			}
		})
	}
}

func TestCommandUsageErrors(t *testing.T) {
	root := newTempDir(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "expected init or upgrade"},
		{"unknown subcommand", []string{"frobnicate"}, "expected init or upgrade"},
		{"unknown option", []string{"init", "--project", root, "--nope", "x"}, "unknown, repeated, or inapplicable option"},
		{"missing required project", []string{"upgrade"}, "upgrade requires --project"},
		{"repeated switch", []string{"upgrade", "--project", root, "--apply", "--apply"}, "unknown, repeated, or inapplicable option"},
		{"missing option value", []string{"init", "--project"}, "missing or invalid value for --project"},
		{"value that looks like a flag", []string{"init", "--project", "--explicit-1c-project"}, "missing or invalid value for --project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, _, errOut := runCLI(t, cliTestDeps("0.2.0"), test.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr: %s", code, errOut)
			}
			if !strings.HasPrefix(errOut, "BF_INVALID: ") || !strings.Contains(errOut, test.want) {
				t.Fatalf("stderr = %q, want BF_INVALID containing %q", errOut, test.want)
			}
		})
	}
}

func TestCommandFailsClosedWithoutTemplates(t *testing.T) {
	root := newTempDir(t)
	code, _, errOut := runCLI(t, cliDeps{}, "init", "--project", root, "--explicit-1c-project")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.HasPrefix(errOut, "BF_BLOCKED: ") || !strings.Contains(errOut, "templates are unavailable") {
		t.Fatalf("stderr = %q", errOut)
	}
	if fileExists(filepath.Join(root, "AGENTS.md")) {
		t.Error("fail-closed init created managed files")
	}

	code, _, errOut = runCLI(t, cliDeps{}, "upgrade", "--project", root, "--apply")
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.HasPrefix(errOut, "BF_BLOCKED: ") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestCommandInitSkipProjectUpgradePreservesExistingTextFiles(t *testing.T) {
	root := newTempDir(t)
	writeUpgradeTargetProject(t, root)

	// The existing sentinel confirms the 1C project without the explicit flag.
	code, out, errOut := runCLI(t, cliTestDeps("0.2.0"), "init", "--project", root, "--skip-project-upgrade")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	if strings.Contains(out, `"schema_version"`) {
		t.Errorf("skip-upgrade run printed an upgrade plan; got:\n%s", out)
	}
	if got := readProjectFile(t, root, "bsl-flow.yaml"); got != upgradeTestConfig {
		t.Error("skip-upgrade run merged bsl-flow.yaml")
	}
	if got := readProjectFile(t, root, "AGENTS.md"); got != upgradeTestAgents {
		t.Error("skip-upgrade run merged AGENTS.md")
	}
	if got := readProjectFile(t, root, ".bsl-flow/project.yaml"); got != upgradeTestSentinel {
		t.Error("skip-upgrade run rewrote the sentinel")
	}
	if got := readProjectFile(t, root, ".gitignore"); got != "user rules only\n\n# bsl-flow managed:start\n.bsl-flow/reports/*\n!.bsl-flow/reports/.gitkeep\n.bsl-flow/tasks/\n# bsl-flow managed:end\n" {
		t.Errorf("skip-upgrade run left .gitignore without the managed block:\n%q", got)
	}
}

func TestCommandInitRunsReadonlyUpgradePreflight(t *testing.T) {
	root := newTempDir(t)
	writeUpgradeTargetProject(t, root)

	code, out, errOut := runCLI(t, cliTestDeps("0.2.0"), "init", "--project", root)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s, stdout: %s", code, errOut, out)
	}
	// The preflight plan (apply_requested=false) prints before the applied
	// plan, mirroring the Write-Host behavior the PS script cannot suppress.
	if !strings.Contains(out, `"apply_requested": false`) || !strings.Contains(out, `"status": "changes_planned"`) {
		t.Errorf("init output lacks the read-only preflight plan; got:\n%s", out)
	}
}

func TestCompareBFVersionPortsPowerShellOrdering(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"0.8.0", "0.8.0", 0},
		{"1.0", "1.0.0", 0},
		{"0.8.0", "0.9.0", -1},
		{"0.9.0", "0.8.0", 1},
		{"0.8.0", "0.8.0-dev.2", 1},
		{"0.8.0-dev.2", "0.8.0", -1},
		{"0.8.0-dev.3", "0.8.0-dev.2", 1},
		{"0.8.0-dev.2", "0.8.0-dev.10", -1},
		{"0.8.0-dev.2", "0.8.0-alpha.1", 1},
		{"0.8.0-1", "0.8.0-alpha", -1},
		{"0.8.0-dev", "0.8.0-dev.1", -1},
	}
	for _, test := range tests {
		got, err := compareBFVersion(test.left, test.right)
		if err != nil {
			t.Fatalf("compareBFVersion(%s, %s): %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Errorf("compareBFVersion(%s, %s) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
	if _, err := compareBFVersion("not-a-version", "1.0.0"); err == nil || !strings.Contains(err.Error(), "Unsupported framework_version") {
		t.Fatalf("malformed version error = %v", err)
	}
}

func TestUpdatedSentinelVersionByteCompatibility(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "replace quoted value",
			text: "format_version: 1\nframework: bsl-flow\nframework_version: \"0.1.0\"\ninitialized_at: \"x\"\n",
			want: "format_version: 1\nframework: bsl-flow\nframework_version: \"0.2.0\"\ninitialized_at: \"x\"\n",
		},
		{
			name: "replace unquoted value",
			text: "framework: bsl-flow\nframework_version: 0.1.0\n",
			want: "framework: bsl-flow\nframework_version: \"0.2.0\"\n",
		},
		{
			name: "keep trailing comment",
			text: "framework: bsl-flow\nframework_version: 0.1.0 # pinned\n",
			want: "framework: bsl-flow\nframework_version: \"0.2.0\" # pinned\n",
		},
		{
			name: "keep indentation",
			text: "framework: bsl-flow\n  framework_version: \"0.1.0\"\n",
			want: "framework: bsl-flow\n  framework_version: \"0.2.0\"\n",
		},
		{
			name: "append with LF",
			text: "framework: bsl-flow\n",
			want: "framework: bsl-flow\nframework_version: \"0.2.0\"\n",
		},
		{
			name: "append with CRLF",
			text: "framework: bsl-flow\r\n",
			want: "framework: bsl-flow\r\nframework_version: \"0.2.0\"\r\n",
		},
		{
			name: "CRLF replace",
			text: "framework_version: \"0.1.0\"\r\ninitialized_at: \"x\"\r\n",
			want: "framework_version: \"0.2.0\"\r\ninitialized_at: \"x\"\r\n",
		},
		{
			name: "same version stays byte-identical",
			text: "framework_version: \"0.2.0\"\n",
			want: "framework_version: \"0.2.0\"\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := updatedSentinelVersion(test.text, "0.2.0")
			if err != nil {
				t.Fatalf("updatedSentinelVersion: %v", err)
			}
			if got != test.want {
				t.Fatalf("updatedSentinelVersion:\n%q\nwant:\n%q", got, test.want)
			}
		})
	}
	if _, err := updatedSentinelVersion("framework_version: \"1\"\nframework_version: \"2\"\n", "0.2.0"); err == nil || !strings.Contains(err.Error(), "duplicate framework_version") {
		t.Fatalf("duplicate version error = %v", err)
	}
}

func TestConfirmed1CProjectDetection(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  bool
	}{
		{name: "empty directory", setup: func(t *testing.T, root string) {}, want: false},
		{
			name: "strong file in root",
			setup: func(t *testing.T, root string) {
				writeProjectFile(t, root, "ConfigDumpInfo.xml", "<dump/>")
			},
			want: true,
		},
		{
			name: "bsl extension in root",
			setup: func(t *testing.T, root string) {
				writeProjectFile(t, root, "Module.bsl", "// code")
			},
			want: true,
		},
		{
			name: "strong directory in root",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "CommonModules"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "two source directories",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, "cf"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "indicator nested in single source directory",
			setup: func(t *testing.T, root string) {
				writeProjectFile(t, root, "src/Configurations/Main/Configuration.xml", "<cfg/>")
			},
			want: true,
		},
		{
			name: "single empty source directory",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTempDir(t)
			test.setup(t, root)
			if got := confirmed1CProject(root, false); got != test.want {
				t.Errorf("confirmed1CProject = %v, want %v", got, test.want)
			}
			if !confirmed1CProject(root, true) {
				t.Error("explicit flag must always confirm")
			}
		})
	}
}
