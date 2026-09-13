package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=BSL Flow Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=BSL Flow Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	// Warnings on stderr (for example an unreadable global ignore) must never
	// be mistaken for command output.
	return strings.TrimSpace(stdout.String())
}

// fullV1State returns a valid closed-schema v1 controller state fixture.
func fullV1State(id string, overrides map[string]any) map[string]any {
	state := map[string]any{
		"schema_version":  1,
		"task_id":         id,
		"revision":        1,
		"previous_sha256": nil,
		"project_path":    "C:/tmp/project",
		"worker_path":     "C:/tmp/project/.bsl-flow/worktrees/x",
		"baseline":        "0123456789abcdef0123456789abcdef01234567",
		"request_hash":    "aa",
		"intent_hash":     "bb",
		"policy_hash":     "cc",
		"request": map[string]any{
			"schema_version": 1,
			"request_id":     id,
			"prompt":         "legacy prompt",
			"mode":           "implement",
			"analysis_goal":  "analysis",
			"complexity":     "S",
			"risk":           "low",
			"impact_flags":   []any{},
			"criteria":       []any{map[string]any{"id": "legacy", "observation": "legacy criterion", "kind": "file_assertion", "path": "legacy.txt", "contains": "legacy"}},
			"provenance":     map[string]any{"source": "user", "reference": "legacy-fixture", "text": "legacy fixture"},
			"models":         map[string]any{"worker": "gpt-6-astra", "worker_effort": "medium", "reviewer": "gpt-6-astra", "reviewer_effort": "high"},
		},
		"intent_revision":        1,
		"authorization_revision": 1,
		"correction_rounds":      0,
		"policy_files":           []any{},
		"attempts":               []any{},
		"evidence":               []any{},
		"events":                 []any{},
		"blockers":               []any{},
		"acceptances":            []any{},
		"policy_rules":           map[string]any{},
		"classification":         map[string]any{},
		"status":                 "ready",
		"stage":                  "inspect",
		"active_attempt":         nil,
		"unresolved_effect":      nil,
		"question":               nil,
		"created_at":             "2026-01-01T00:00:00Z",
		"updated_at":             "2026-01-01T00:00:00Z",
	}
	for key, value := range overrides {
		state[key] = value
	}
	return state
}

// tempDir avoids testing.TempDir's cleanup failing on Windows when a transient
// antivirus or indexer handle briefly keeps a freshly written file open.
func tempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "bsl-flow-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func newRepo(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(tempDir(t), "repo")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "init")
	if err := os.WriteFile(filepath.Join(directory, "readme.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", ".")
	runGit(t, directory, "commit", "-m", "fixture")
	return directory
}

func writeJSON(t *testing.T, directory, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func createTask(t *testing.T, project string, input map[string]any) map[string]any {
	t.Helper()
	inputPath := writeJSON(t, tempDir(t), "create.json", input)
	payload, err := commandCreate(project, inputPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return payload.(map[string]any)
}

func TestRepositoryIdentitySharedAcrossWorktrees(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "shared task"})
	taskID := created["task_id"].(string)

	linked := filepath.Join(tempDir(t), "linked")
	runGit(t, project, "worktree", "add", "-b", "linked", linked)

	repository, err := openReady(linked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository.CommonDir, "bsl-flow", "repository.json")); err != nil {
		t.Fatalf("repository identity missing: %v", err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != taskID {
		t.Fatalf("worktree did not share the repository store: %+v", rows)
	}
	if runGit(t, project, "status", "--porcelain") != "" {
		t.Fatal("registry files leaked into git status")
	}

	clone := filepath.Join(tempDir(t), "clone")
	runGit(t, filepath.Dir(clone), "clone", project, clone)
	cloneRepository, err := openReady(clone)
	if err != nil {
		t.Fatal(err)
	}
	cloneRows, _, err := cloneRepository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(cloneRows) != 0 {
		t.Fatalf("independent clone saw repository tasks: %+v", cloneRows)
	}
}

func TestRepositoryResolutionIgnoresInheritedGitRouting(t *testing.T) {
	project := newRepo(t)
	clone := filepath.Join(tempDir(t), "clone")
	runGit(t, filepath.Dir(clone), "clone", project, clone)
	projectRepository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	cloneRepository, err := openReady(clone)
	if err != nil {
		t.Fatal(err)
	}
	if projectRepository.CommonDir == cloneRepository.CommonDir {
		t.Fatal("temporary clone unexpectedly shares the source common dir")
	}
	projectID := createTask(t, project, map[string]any{"schema_version": 1, "title": "source"})["task_id"].(string)
	cloneID := createTask(t, clone, map[string]any{"schema_version": 1, "title": "clone"})["task_id"].(string)

	t.Setenv("GIT_DIR", cloneRepository.CommonDir)
	t.Setenv("GIT_WORK_TREE", clone)
	t.Setenv("GIT_COMMON_DIR", cloneRepository.CommonDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(cloneRepository.CommonDir, "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(cloneRepository.CommonDir, "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(cloneRepository.CommonDir, "objects"))

	resolvedProject, err := OpenRepository(project)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedProject.CommonDir != projectRepository.CommonDir {
		t.Fatalf("inherited Git environment redirected source repository: got=%q want=%q", resolvedProject.CommonDir, projectRepository.CommonDir)
	}
	rows, _, err := resolvedProject.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != projectID {
		t.Fatalf("source catalog was redirected by inherited Git environment: %+v", rows)
	}
	resolvedClone, err := OpenRepository(clone)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = resolvedClone.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != cloneID {
		t.Fatalf("clone catalog changed under inherited Git environment: %+v", rows)
	}
}

func TestTaskLifecycleAndReads(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{
		"schema_version": 1,
		"title":          "first task",
		"description":    "details",
		"priority":       "high",
		"labels":         []string{"alpha", "beta"},
	})
	id := created["task_id"].(string)
	if revision, ok := asInt(created["revision"]); !ok || revision != 1 || created["status"] != "planned" {
		t.Fatalf("unexpected create envelope: %+v", created)
	}

	patch := writeJSON(t, tempDir(t), "edit.json", map[string]any{"title": "renamed", "priority": "critical"})
	if _, err := commandEdit(project, id, "1", patch); err != nil {
		t.Fatal(err)
	}

	list, err := commandList(&options{values: map[string]string{"--project": project, "--priority": "critical"}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := list.(map[string]any)["tasks"].([]Row)
	if len(tasks) != 1 || tasks[0].Title != "renamed" || tasks[0].Revision != 2 {
		t.Fatalf("unexpected list: %+v", tasks)
	}

	archived := list
	archived, err = commandList(&options{values: map[string]string{"--project": project, "--archived": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived.(map[string]any)["tasks"].([]Row)) != 0 {
		t.Fatal("archived filter returned active tasks")
	}

	shown, err := commandShow(project, id)
	if err != nil {
		t.Fatal(err)
	}
	if shown.(map[string]any)["task"].(Row).Title != "renamed" {
		t.Fatalf("show did not return the latest revision: %+v", shown)
	}

	history, err := commandHistory(project, id)
	if err != nil {
		t.Fatal(err)
	}
	events := history.(map[string]any)["events"].([]map[string]any)
	if len(events) != 2 || events[1]["event"] != "updated" {
		t.Fatalf("unexpected history: %+v", events)
	}

	overview, err := commandOverview(&options{values: map[string]string{"--project": project}})
	if err != nil {
		t.Fatal(err)
	}
	if overview.(map[string]any)["total"] != 1 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
}

func TestExpectedRevisionConflict(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "conflict"})
	id := created["task_id"].(string)
	patch := writeJSON(t, tempDir(t), "edit.json", map[string]any{"title": "other"})
	if _, err := commandEdit(project, id, "5", patch); err == nil {
		t.Fatal("stale expected revision was accepted")
	}
}

func TestDependencyCycleAndSelfReferenceRejected(t *testing.T) {
	project := newRepo(t)
	a := createTask(t, project, map[string]any{"schema_version": 1, "title": "a"})["task_id"].(string)
	b := createTask(t, project, map[string]any{"schema_version": 1, "title": "b"})["task_id"].(string)
	patchA := writeJSON(t, tempDir(t), "a.json", map[string]any{"depends_on": []string{b}})
	if _, err := commandEdit(project, a, "1", patchA); err != nil {
		t.Fatal(err)
	}
	patchB := writeJSON(t, tempDir(t), "b.json", map[string]any{"depends_on": []string{a}})
	if _, err := commandEdit(project, b, "1", patchB); err == nil {
		t.Fatal("dependency cycle was accepted")
	}
	self := writeJSON(t, tempDir(t), "self.json", map[string]any{"depends_on": []string{a}})
	if _, err := commandEdit(project, a, "2", self); err == nil {
		t.Fatal("self dependency was accepted")
	}
}

func TestCorruptTaskIsDiagnosticNotFatal(t *testing.T) {
	project := newRepo(t)
	good := createTask(t, project, map[string]any{"schema_version": 1, "title": "good"})["task_id"].(string)
	bad := createTask(t, project, map[string]any{"schema_version": 1, "title": "bad"})["task_id"].(string)
	badRevision := filepath.Join(project, ".git", "bsl-flow", "tasks", bad, "revisions", "000001.json")
	if err := os.WriteFile(badRevision, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != good {
		t.Fatalf("valid task hidden by corrupt journal: %+v", rows)
	}
	if len(diagnostics) != 1 || diagnostics[0]["health"] != "corrupt" {
		t.Fatalf("corrupt task not reported: %+v", diagnostics)
	}
}

func TestArchiveAndUnarchiveArePresentationFlags(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "archivable"})
	id := created["task_id"].(string)
	if _, err := commandArchive(project, id, "1", true); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if !task.Archived || task.Lifecycle != "planned" {
		t.Fatalf("archive changed lifecycle: %+v", task)
	}
	if _, err := commandArchive(project, id, "2", false); err != nil {
		t.Fatal(err)
	}
	task, err = repository.ReadTask(id)
	if err != nil || task.Archived {
		t.Fatalf("unarchive failed: %+v", task)
	}
}

func TestLegacyDiscoveryIsReadOnly(t *testing.T) {
	project := newRepo(t)
	legacyID := "11111111-2222-4333-8444-555555555555"
	legacyDir := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := fullV1State(legacyID, nil)
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "legacy" || rows[0].Status != "ready" {
		t.Fatalf("legacy task not discovered: %+v", rows)
	}
	after, err := os.ReadFile(filepath.Join(legacyDir, "000001.json"))
	if err != nil || string(after) != string(data) {
		t.Fatal("legacy journal bytes were rewritten")
	}
}

func TestLegacyDuplicateDeduplicatedAndDivergenceIsConflict(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "shared"})
	id := created["task_id"].(string)
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repository.StorePath, "tasks", id, "revisions", "000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(project, ".bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyDir, "000001.json")
	if err := os.WriteFile(legacyFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "repository" || len(diagnostics) != 0 {
		t.Fatalf("identical legacy history was not deduplicated: rows=%+v diagnostics=%+v", rows, diagnostics)
	}

	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["title"] = "divergent"
	divergent, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, divergent, 0o600); err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err = repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "repository" {
		t.Fatalf("repository row lost: %+v", rows)
	}
	if len(diagnostics) != 1 || diagnostics[0]["health"] != "conflict" {
		t.Fatalf("divergent legacy history was not reported as conflict: %+v", diagnostics)
	}
}

func TestDispatchEnvelopeAndExitCodes(t *testing.T) {
	project := newRepo(t)
	inputPath := writeJSON(t, tempDir(t), "create.json", map[string]any{"schema_version": 1, "title": "cli"})
	var out bytes.Buffer
	handled, code := Dispatch([]string{"task", "create", "--project", project, "--input", inputPath}, &out, &out)
	if !handled || code != 0 {
		t.Fatalf("create exit %d: %s", code, out.String())
	}
	var created map[string]any
	if err := json.Unmarshal(out.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["status"] != "planned" {
		t.Fatalf("unexpected create envelope: %s", out.String())
	}

	out.Reset()
	handled, code = Dispatch([]string{"task", "list", "--project", project, "--status", "planned"}, &out, &out)
	if !handled || code != 0 || !strings.Contains(out.String(), "\"tasks\"") {
		t.Fatalf("list exit %d: %s", code, out.String())
	}

	out.Reset()
	handled, code = Dispatch([]string{"task", "show", "--project", project, "--task", "not-a-uuid"}, &out, &out)
	if !handled || code != 2 || !strings.Contains(out.String(), "BF_INVALID") {
		t.Fatalf("invalid exit %d: %s", code, out.String())
	}

	out.Reset()
	handled, code = Dispatch([]string{"task", "activate", "--project", project, "--task", created["task_id"].(string)}, &out, &out)
	if !handled || code != 11 || !strings.Contains(out.String(), "BF_BLOCKED") {
		t.Fatalf("activate exit %d: %s", code, out.String())
	}

	if handled, _ := Dispatch([]string{"task", "status", "--project", project, "--task", created["task_id"].(string)}, &out, &out); handled {
		t.Fatal("legacy command was captured by the native registry")
	}
}

func TestProvenanceRejectsSecretsAndUnknownFields(t *testing.T) {
	project := newRepo(t)
	for _, provenance := range []map[string]any{
		{"source": "Authorization: Bearer abc123"},
		{"author": "ok", "unexpected": "x"},
		{"reference": "my password is hunter2"},
	} {
		input := writeJSON(t, tempDir(t), "create.json", map[string]any{"schema_version": 1, "title": "p", "provenance": provenance})
		if _, err := commandCreate(project, input); err == nil {
			t.Fatalf("accepted unsafe provenance: %+v", provenance)
		}
	}
	input := writeJSON(t, tempDir(t), "create.json", map[string]any{"schema_version": 1, "title": "p", "provenance": map[string]any{"author": "team", "source": "issue-1"}})
	if _, err := commandCreate(project, input); err != nil {
		t.Fatalf("rejected safe provenance: %v", err)
	}
}

func TestSemanticCorruptionIsDiagnosticAndShowBlocks(t *testing.T) {
	project := newRepo(t)
	id := "22222222-3333-4444-8555-666666666666"
	revisionDir := filepath.Join(project, ".git", "bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(revisionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"schema_version": 2, "task_id": id, "revision": 1, "previous_sha256": nil, "title": "", "priority": "low", "labels": []any{}, "depends_on": []any{}, "lifecycle": "planned", "archived": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "provenance": map[string]any{}}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revisionDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("semantically corrupt task was listed as valid: %+v", rows)
	}
	if len(diagnostics) != 1 || diagnostics[0]["health"] != "corrupt" {
		t.Fatalf("semantic corruption not reported: %+v", diagnostics)
	}
	if _, err := commandShow(project, id); err == nil {
		t.Fatal("show accepted a semantically corrupt task")
	}
}

func TestLegacyAcrossWorktreesAndShowHistory(t *testing.T) {
	project := newRepo(t)
	linked := filepath.Join(tempDir(t), "linked")
	runGit(t, project, "worktree", "add", "-b", "linked", linked)

	legacyID := "33333333-4444-4555-8666-777777777777"
	legacyDir := filepath.Join(linked, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := fullV1State(legacyID, map[string]any{"project_path": linked})
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != legacyID || rows[0].Source != "legacy" {
		t.Fatalf("legacy task from another worktree not discovered: %+v", rows)
	}
	if strings.Contains(rows[0].Title, "secret") {
		t.Fatalf("legacy prompt leaked into the catalog title: %q", rows[0].Title)
	}
	if rows[0].OriginWorktree != linked {
		t.Fatalf("origin worktree is not the discovery location: %q", rows[0].OriginWorktree)
	}
	shown, err := commandShow(project, legacyID)
	if err != nil {
		t.Fatalf("legacy show failed: %v", err)
	}
	if shown.(map[string]any)["task"].(Row).Source != "legacy" {
		t.Fatalf("legacy show did not resolve: %+v", shown)
	}
	if _, present := shown.(map[string]any)["provenance"]; present {
		t.Fatal("legacy show exposed provenance")
	}
	history, err := commandHistory(project, legacyID)
	if err != nil {
		t.Fatalf("legacy history failed: %v", err)
	}
	if len(history.(map[string]any)["events"].([]map[string]any)) != 1 {
		t.Fatalf("legacy history malformed: %+v", history)
	}
}

func TestCursorIsolationByRepositoryAndFilters(t *testing.T) {
	project := newRepo(t)
	for _, title := range []string{"one", "two", "three"} {
		createTask(t, project, map[string]any{"schema_version": 1, "title": title})
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	selected, next, err := applyFilters(rows, Filters{Limit: 1, Archived: boolPtr(false)}, repository.CloneID)
	if err != nil || len(selected) != 1 || next == "" {
		t.Fatalf("first page failed: %v %v %q", err, len(selected), next)
	}
	foreign := encodeCursor("00000000-0000-4000-8000-000000000000", filterSignature(Filters{Archived: boolPtr(false)}), cursorKey(selected[0]))
	if _, _, err := applyFilters(rows, Filters{Cursor: foreign, Archived: boolPtr(false)}, repository.CloneID); err == nil {
		t.Fatal("accepted a cursor from a different repository")
	}
	if _, _, err := applyFilters(rows, Filters{Cursor: next, Priority: "high", Archived: boolPtr(false)}, repository.CloneID); err == nil {
		t.Fatal("accepted a cursor with mismatched filters")
	}
	if _, _, err := applyFilters(rows, Filters{Cursor: next, Archived: boolPtr(true)}, repository.CloneID); err == nil {
		t.Fatal("accepted a cursor with mismatched archived filter")
	}
}

func TestHumanOutputEscapesControlSequences(t *testing.T) {
	output := humanOutput(map[string]any{"tasks": []Row{{TaskID: "id", Title: "a\x1b[31mb", Status: "planned", Priority: "low", UpdatedAt: "2026-01-01"}}})
	if strings.Contains(output, "\x1b") {
		t.Fatal("raw terminal escape sequence reached human output")
	}
	if !strings.Contains(output, `\x1b`) {
		t.Fatalf("escape sequence was not rendered visibly: %q", output)
	}
	errorMessage := sanitize("bad\x00value")
	if strings.Contains(errorMessage, "\x00") {
		t.Fatal("control character survived sanitisation")
	}
}

func TestStoreSurvivesWorktreeDeletion(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "durable"})
	id := created["task_id"].(string)
	linked := filepath.Join(tempDir(t), "linked")
	runGit(t, project, "worktree", "add", "-b", "linked", linked)
	runGit(t, project, "worktree", "remove", linked)
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != id {
		t.Fatalf("task lost after worktree deletion: %+v", rows)
	}
}

func TestStorePreservesDeletedOriginWorktree(t *testing.T) {
	project := newRepo(t)
	linked := filepath.Join(tempDir(t), "origin")
	runGit(t, project, "worktree", "add", "-b", "origin", linked)
	created := createTask(t, linked, map[string]any{"schema_version": 1, "title": "origin task"})
	id := created["task_id"].(string)
	runGit(t, project, "worktree", "remove", linked)

	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != id {
		t.Fatalf("task created in the deleted origin worktree was lost: %+v", rows)
	}
	if rows[0].OriginWorktree != linked {
		t.Fatalf("deleted origin worktree was replaced by current reader: got=%q want=%q", rows[0].OriginWorktree, linked)
	}
}

func TestRunOnPlannedRepositoryTaskIsActivationBlocker(t *testing.T) {
	project := newRepo(t)
	id := createTask(t, project, map[string]any{"schema_version": 1, "title": "planned"})["task_id"].(string)
	var out, errOut bytes.Buffer
	handled, code := Dispatch([]string{"task", "run", "--project", project, "--task", id}, &out, &errOut)
	if !handled || code != 11 || !strings.Contains(out.String(), "BF_BLOCKED") {
		t.Fatalf("run accepted a planned repository task: handled=%v code=%d out=%s", handled, code, out.String())
	}
	out.Reset()
	errOut.Reset()
	handled, code = Dispatch([]string{"task", "run", "--project", project, "--task", id, "--human"}, &out, &errOut)
	if !handled || code != 11 || out.Len() != 0 || !strings.Contains(errOut.String(), "BF_BLOCKED") {
		t.Fatalf("human run blocker wrong: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if handled, _ := Dispatch([]string{"task", "run", "--project", project, "--task", "44444444-5555-4666-8777-888888888888"}, &out, &errOut); handled {
		t.Fatal("unknown repository task was captured instead of delegated")
	}
}

func TestConcurrentOpposingEdgesSerialize(t *testing.T) {
	project := newRepo(t)
	a := createTask(t, project, map[string]any{"schema_version": 1, "title": "a"})["task_id"].(string)
	b := createTask(t, project, map[string]any{"schema_version": 1, "title": "b"})["task_id"].(string)
	patchA := writeJSON(t, tempDir(t), "a.json", map[string]any{"depends_on": []string{b}})
	patchB := writeJSON(t, tempDir(t), "b.json", map[string]any{"depends_on": []string{a}})

	var wait sync.WaitGroup
	results := make([]error, 2)
	wait.Add(2)
	go func() { defer wait.Done(); _, results[0] = commandEdit(project, a, "1", patchA) }()
	go func() { defer wait.Done(); _, results[1] = commandEdit(project, b, "1", patchB) }()
	wait.Wait()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one edge to pass the graph critical section, got %d (%v)", successes, results)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	taskA, err := repository.ReadTask(a)
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := repository.ReadTask(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(taskA.DependsOn) > 0 && len(taskB.DependsOn) > 0 {
		t.Fatal("both opposing edges were committed, creating a cycle")
	}
}

func TestOverviewIncludesStaleAndConflictCounters(t *testing.T) {
	project := newRepo(t)
	createTask(t, project, map[string]any{"schema_version": 1, "title": "overview"})
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	counters := Overview(rows, diagnostics)
	for _, key := range []string{"stale_completed", "corrupt", "conflicts", "orphaned", "dependency_blocked"} {
		if _, present := counters[key]; !present {
			t.Fatalf("overview missing counter %q: %+v", key, counters)
		}
	}
}

func TestMetadataRejectsSecretsAndControlCharacters(t *testing.T) {
	project := newRepo(t)
	for name, input := range map[string]map[string]any{
		"title":       {"schema_version": 1, "title": "Bearer abc123"},
		"description": {"schema_version": 1, "title": "ok", "description": "password=hunter2"},
		"labels":      {"schema_version": 1, "title": "ok", "labels": []string{"api_key"}},
	} {
		path := writeJSON(t, tempDir(t), "create.json", input)
		if _, err := commandCreate(project, path); err == nil {
			t.Fatalf("accepted secrets in %s", name)
		}
	}
	ordinaryProject := newRepo(t)
	ordinary := writeJSON(t, tempDir(t), "ordinary.json", map[string]any{
		"schema_version": 1,
		"title":          "Fix Authorization header handling",
		"description":    "Проверить пароль",
	})
	if _, err := commandCreate(ordinaryProject, ordinary); err != nil {
		t.Fatalf("rejected ordinary security terminology in metadata: %v", err)
	}
	// Existing journals with secrets in card fields must surface as corrupt,
	// not silently return through show/list.
	id := "55555555-6666-4777-8888-999999999999"
	revisionDir := filepath.Join(project, ".git", "bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(revisionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"schema_version": 2, "task_id": id, "revision": 1, "previous_sha256": nil, "title": "password=hunter2", "priority": "low", "labels": []any{}, "depends_on": []any{}, "lifecycle": "planned", "archived": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "provenance": map[string]any{}}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revisionDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(diagnostics) != 1 || diagnostics[0]["health"] != "corrupt" {
		t.Fatalf("journal with secrets was not treated as corrupt: rows=%+v diagnostics=%+v", rows, diagnostics)
	}
	if _, err := commandShow(project, id); err == nil {
		t.Fatal("show accepted a journal with secrets in card fields")
	}
}

func TestProjectionRedactsCredentialFieldsAsWholeValues(t *testing.T) {
	quoted := `{"password":"hunter2"}`
	if projected := safeProjectionText(quoted); projected != "[REDACTED]" || strings.Contains(projected, "hunter2") {
		t.Fatalf("quoted credential was not fully redacted: %q", projected)
	}
	pem := "prefix\n-----BEGIN RSA PRIVATE KEY-----\nYWJjZGVmZ2hpamtsbW5vcA==\n-----END RSA PRIVATE KEY-----\nsuffix"
	if projected := safeProjectionText(pem); projected != "[REDACTED]" || strings.Contains(projected, "YWJjZGVm") {
		t.Fatalf("PEM material was not fully redacted: %q", projected)
	}
	if projected := safeProjectionText("Fix Authorization header handling; Проверить пароль"); projected != "Fix Authorization header handling; Проверить пароль" {
		t.Fatalf("ordinary security terminology was redacted: %q", projected)
	}
	var stdout, stderr bytes.Buffer
	writeError(&stdout, &stderr, blocked("legacy value: %s", quoted), false)
	if strings.Contains(stdout.String(), "hunter2") || strings.Contains(stderr.String(), "hunter2") {
		t.Fatalf("credential leaked through error output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestV1RequestRequiredFieldsAreChecked(t *testing.T) {
	project := newRepo(t)
	id := "abababab-cdcd-4efe-8a8a-121212121212"
	state := fullV1State(id, nil)
	delete(state["request"].(map[string]any), "models")
	revisionDir := filepath.Join(project, ".git", "bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(revisionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revisionDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(diagnostics) != 1 || diagnostics[0]["health"] != "corrupt" {
		t.Fatalf("v1 request with missing required field was accepted: rows=%+v diagnostics=%+v", rows, diagnostics)
	}
}

func TestLegacyTimestampCredentialIsBlockedWithoutLeak(t *testing.T) {
	project := newRepo(t)
	id := "23232323-4545-4678-8999-bbbbbbbbbbbb"
	legacyDir := filepath.Join(project, ".bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := fullV1State(id, map[string]any{"updated_at": "password=REGISTRY-REVIEW-SYNTHETIC"})
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"show", "history"} {
		var stdout, stderr bytes.Buffer
		handled, code := Dispatch([]string{"task", action, "--project", project, "--task", id}, &stdout, &stderr)
		if !handled || code != 11 {
			t.Fatalf("%s accepted invalid legacy timestamp: handled=%v code=%d stdout=%q stderr=%q", action, handled, code, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "REGISTRY-REVIEW-SYNTHETIC") || strings.Contains(stderr.String(), "REGISTRY-REVIEW-SYNTHETIC") {
			t.Fatalf("%s leaked invalid timestamp: stdout=%q stderr=%q", action, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "BF_BLOCKED") {
			t.Fatalf("%s did not return a controller-shaped blocker: %q", action, stdout.String())
		}
	}
}

func TestSyntheticV1StateIsRejected(t *testing.T) {
	project := newRepo(t)
	id := "66666666-7777-4888-8999-aaaaaaaaaaaa"
	revisionDir := filepath.Join(project, ".git", "bsl-flow", "tasks", id, "revisions")
	if err := os.MkdirAll(revisionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Minimal but hash-valid v1 record: correct chain, string status, no
	// controller fields. It must never enter the catalog as healthy.
	state := map[string]any{"schema_version": 1, "task_id": id, "revision": 1, "previous_sha256": nil, "status": "completed", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revisionDir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(diagnostics) != 1 || diagnostics[0]["health"] != "corrupt" {
		t.Fatalf("synthetic v1 state was accepted as healthy: rows=%+v diagnostics=%+v", rows, diagnostics)
	}
	if _, err := commandShow(project, id); err == nil {
		t.Fatal("show accepted a synthetic v1 state")
	}
	if _, err := commandHistory(project, id); err == nil {
		t.Fatal("history accepted a synthetic v1 state")
	}
}

func TestBrokenLegacyJournalBlocksShowAndHistory(t *testing.T) {
	project := newRepo(t)
	legacyID := "77777777-8888-4999-8aaa-bbbbbbbbbbbb"
	legacyDir := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "000001.json"), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := commandShow(project, legacyID); err == nil {
		t.Fatal("show ignored an unreadable legacy journal")
	} else if typed, ok := err.(*KindError); !ok || typed.Kind != "BF_BLOCKED" {
		t.Fatalf("show error kind is not BF_BLOCKED: %v", err)
	}
	if _, err := commandHistory(project, legacyID); err == nil {
		t.Fatal("history ignored an unreadable legacy journal")
	} else if typed, ok := err.(*KindError); !ok || typed.Kind != "BF_BLOCKED" {
		t.Fatalf("history error kind is not BF_BLOCKED: %v", err)
	}
}

func TestCanonicalInvalidIdentityBlocksHealthyLegacyFallback(t *testing.T) {
	for _, kind := range []string{"corrupt", "orphaned"} {
		t.Run(kind, func(t *testing.T) {
			project := newRepo(t)
			repository, err := openReady(project)
			if err != nil {
				t.Fatal(err)
			}
			id := "12121212-3434-4567-8899-aaaaaaaaaaaa"
			canonicalDir := filepath.Join(repository.StorePath, "tasks", id)
			if kind == "corrupt" {
				revisionDir := filepath.Join(canonicalDir, "revisions")
				if err := os.MkdirAll(revisionDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(revisionDir, "000001.json"), []byte("{torn"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(canonicalDir, 0o700); err != nil {
				t.Fatal(err)
			}

			legacyDir := filepath.Join(project, ".bsl-flow", "tasks", id, "revisions")
			if err := os.MkdirAll(legacyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			data, err := Canonical(fullV1State(id, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacyDir, "000001.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}

			rows, diagnostics, err := repository.Catalog()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 || len(diagnostics) != 1 || diagnostics[0]["health"] != kind {
				t.Fatalf("healthy legacy fallback replaced invalid canonical identity: rows=%+v diagnostics=%+v", rows, diagnostics)
			}
			if _, err := commandShow(project, id); err == nil {
				t.Fatal("show silently fell back to legacy task")
			} else if typed, ok := err.(*KindError); !ok || typed.Kind != "BF_BLOCKED" {
				t.Fatalf("show error kind is not BF_BLOCKED: %v", err)
			}
		})
	}
}

func TestDivergentLegacyHistoriesBlockDetailReads(t *testing.T) {
	project := newRepo(t)
	legacyID := "88888888-9999-4aaa-8bbb-cccccccccccc"
	legacyState := fullV1State(legacyID, nil)
	data, err := Canonical(legacyState)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(tempDir(t), "linked")
	runGit(t, project, "worktree", "add", "-b", "linked", linked)
	for _, root := range []string{project, linked} {
		dir := filepath.Join(root, ".bsl-flow", "tasks", legacyID, "revisions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "000001.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	divergent := fullV1State(legacyID, map[string]any{"status": "completed", "stage": "acceptance"})
	divergentData, err := Canonical(divergent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".bsl-flow", "tasks", legacyID, "revisions", "000001.json"), divergentData, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range diagnostics {
		if entry["task_id"] == legacyID && entry["health"] == "conflict" {
			found = true
		}
	}
	if !found {
		t.Fatalf("divergent legacy histories were not reported: %+v", diagnostics)
	}
	if _, err := commandShow(project, legacyID); err == nil {
		t.Fatal("show silently picked one divergent legacy copy")
	} else if typed, ok := err.(*KindError); !ok || typed.Kind != "BF_CONFLICT" {
		t.Fatalf("show error kind is not BF_CONFLICT: %v", err)
	}
	if _, err := commandHistory(project, legacyID); err == nil {
		t.Fatal("history silently picked one divergent legacy copy")
	}
}

func TestLegacyControllerProjection(t *testing.T) {
	project := newRepo(t)
	legacyID := "99999999-aaaa-4bbb-8ccc-dddddddddddd"
	state := fullV1State(legacyID, map[string]any{
		"status":         "needs_input",
		"stage":          "verify",
		"question":       map[string]any{"question_id": "q1", "text": "Which base?"},
		"attempts":       []any{map[string]any{"attempt_id": "a1"}},
		"acceptances":    []any{map[string]any{"sha256": "x"}},
		"evidence":       []any{map[string]any{"attempt_id": "a1", "stage": "verify"}, map[string]any{"attempt_id": "a2", "stage": "implement"}},
		"blockers":       []any{"waiting for input"},
		"active_attempt": nil,
	})
	dir := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("legacy controller task not listed: %+v", rows)
	}
	row := rows[0]
	if row.Stage != "verify" || row.NextAction != "unknown" || row.Question != "Which base?" {
		t.Fatalf("controller projection incomplete: %+v", row)
	}
	if row.AttemptCount != 1 || row.AcceptanceCount != 1 || len(row.EvidenceRefs) != 2 {
		t.Fatalf("attempt/acceptance/evidence projection incomplete: %+v", row)
	}
	if len(row.Blockers) != 1 || row.Blockers[0] != "waiting for input" {
		t.Fatalf("blockers not projected: %+v", row.Blockers)
	}
	if strings.Join(row.EvidenceRefs, ",") != "a1,a2" {
		t.Fatalf("evidence refs malformed: %+v", row.EvidenceRefs)
	}
}

func TestCatalogTimestampOrderingAndFiltersUseInstants(t *testing.T) {
	project := newRepo(t)
	fixtures := []struct {
		id      string
		updated string
	}{
		{"13131313-2424-4567-8899-aaaaaaaaaaaa", "2026-09-12T00:00:00.5Z"},
		{"14141414-2525-4567-8899-bbbbbbbbbbbb", "2026-09-12T00:00:00Z"},
		{"15151515-2626-4567-8899-cccccccccccc", "2026-09-12T01:00:00+02:00"},
	}
	for _, fixture := range fixtures {
		directory := filepath.Join(project, ".bsl-flow", "tasks", fixture.id, "revisions")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		state := fullV1State(fixture.id, map[string]any{
			"created_at": fixture.updated,
			"updated_at": fixture.updated,
		})
		data, err := Canonical(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "000001.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 || len(rows) != len(fixtures) {
		t.Fatalf("timestamp fixtures were not read as healthy: rows=%+v diagnostics=%+v", rows, diagnostics)
	}
	ordered := []string{fixtures[0].id, fixtures[1].id, fixtures[2].id}
	for index, id := range ordered {
		if rows[index].TaskID != id {
			t.Fatalf("rows were not sorted by timestamp instant: rows=%+v", rows)
		}
	}
	before, _, err := applyFilters(rows, Filters{UpdatedBefore: "2026-09-12T00:00:00.5Z"}, repository.CloneID)
	if err != nil || len(before) != 2 || before[0].TaskID != fixtures[1].id || before[1].TaskID != fixtures[2].id {
		t.Fatalf("updated-before used lexical comparison: rows=%+v err=%v", before, err)
	}
	after, _, err := applyFilters(rows, Filters{UpdatedAfter: "2026-09-12T00:00:00Z"}, repository.CloneID)
	if err != nil || len(after) != 1 || after[0].TaskID != fixtures[0].id {
		t.Fatalf("updated-after used lexical comparison: rows=%+v err=%v", after, err)
	}
}

func TestOriginWorktreeIsStableAcrossWorktrees(t *testing.T) {
	project := newRepo(t)
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "origined"})
	id := created["task_id"].(string)
	linked := filepath.Join(tempDir(t), "linked")
	runGit(t, project, "worktree", "add", "-b", "linked", linked)

	repository, err := openReady(linked)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != id {
		t.Fatalf("task not visible from linked worktree: %+v", rows)
	}
	if rows[0].OriginWorktree != project {
		t.Fatalf("origin worktree is the reading worktree, not the stored origin: %q", rows[0].OriginWorktree)
	}
}

func TestStaleCompletedCounterReflectsMissingWorker(t *testing.T) {
	project := newRepo(t)
	legacyID := "aaaa1111-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	// worker_path deliberately points at a directory that does not exist.
	state := fullV1State(legacyID, map[string]any{
		"status":      "completed",
		"stage":       "acceptance",
		"worker_path": filepath.Join(project, ".bsl-flow", "worktrees", "gone"),
	})
	dir := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Stale || rows[0].Health != "stale" {
		t.Fatalf("missing worker path was not marked stale: %+v", rows)
	}
	if rows[0].Status != "completed" {
		t.Fatalf("stale diagnostic changed historical status: %+v", rows[0])
	}
	counters := Overview(rows, diagnostics)
	if counters["stale_completed"] != 1 {
		t.Fatalf("stale_completed counter is wrong: %+v", counters)
	}
}

func TestExistingEvidenceFileDoesNotMarkLegacyTaskStale(t *testing.T) {
	project := newRepo(t)
	evidencePath := filepath.Join(project, "evidence.txt")
	if err := os.WriteFile(evidencePath, []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyID := "abab1111-cdcd-4efe-8a8a-343434343434"
	state := fullV1State(legacyID, map[string]any{
		"status":      "completed",
		"stage":       "acceptance",
		"worker_path": project,
		"evidence": []any{map[string]any{
			"raw_hashes": []any{map[string]any{"path": evidencePath}},
		}},
	})
	directory := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 || len(rows) != 1 || rows[0].Stale || rows[0].Health != "ok" {
		t.Fatalf("existing regular evidence file was marked stale: rows=%+v diagnostics=%+v", rows, diagnostics)
	}
}

func TestFullV1FixturesRoundTrip(t *testing.T) {
	project := newRepo(t)
	// Existing worker path, so the fixture is not marked stale.
	worker := filepath.Join(project, ".bsl-flow", "worktrees", "kept")
	if err := os.MkdirAll(worker, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyID := "bbbb2222-cccc-4ddd-8eee-ffffffffffff"
	for _, status := range []string{"completed", "cancelled"} {
		state := fullV1State(legacyID, map[string]any{
			"status":      status,
			"stage":       "acceptance",
			"worker_path": worker,
			"acceptances": []any{map[string]any{"sha256": "x"}},
			"attempts":    []any{map[string]any{"attempt_id": "a1"}},
		})
		dir := filepath.Join(project, ".bsl-flow", "tasks", legacyID, "revisions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := Canonical(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "000001.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		repository, err := openReady(project)
		if err != nil {
			t.Fatal(err)
		}
		rows, diagnostics, err := repository.Catalog()
		if err != nil {
			t.Fatal(err)
		}
		var row *Row
		for index := range rows {
			if rows[index].TaskID == legacyID {
				row = &rows[index]
			}
		}
		if row == nil || row.Status != status || row.Health != "ok" {
			t.Fatalf("retained %s task not read as healthy: rows=%+v diagnostics=%+v", status, rows, diagnostics)
		}
		if row.NextAction != "unknown" {
			t.Fatalf("legacy next action was presented as authoritative for %s: %+v", status, row)
		}
		// Second iteration uses a different UUID to avoid dedup interference.
		legacyID = "cccc3333-dddd-4eee-8fff-000000000001"
	}
}

func TestPublicListIsBoundedAndOverviewCountsFullScope(t *testing.T) {
	project := newRepo(t)
	repository, err := openReady(project)
	if err != nil {
		t.Fatal(err)
	}
	const total = 101
	for index := 1; index <= total; index++ {
		id := fmt.Sprintf("%08x-1111-4222-8333-%012x", index, index)
		card, err := plannedCard(map[string]any{"schema_version": 1, "title": fmt.Sprintf("task-%d", index)}, repository.Worktree)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.writeRevision(id, card, 0); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	handled, code := Dispatch([]string{"task", "list", "--project", project}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("public default list failed: handled=%v code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("default list did not return JSON: %v; output=%q", err, stdout.String())
	}
	tasks, ok := listed["tasks"].([]any)
	if !ok || len(tasks) != 100 {
		t.Fatalf("default list was not bounded to 100 tasks: %+v", listed["tasks"])
	}
	if next, ok := listed["next_cursor"].(string); !ok || next == "" {
		t.Fatalf("default list omitted continuation cursor: %+v", listed["next_cursor"])
	}

	stdout.Reset()
	stderr.Reset()
	handled, code = Dispatch([]string{"task", "overview", "--project", project}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("public overview failed: handled=%v code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
	}
	var overview map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &overview); err != nil {
		t.Fatalf("overview did not return JSON: %v; output=%q", err, stdout.String())
	}
	if got, ok := overview["total"].(float64); !ok || int(got) != total {
		t.Fatalf("overview counted only the bounded page: got=%v want=%d", overview["total"], total)
	}
}

func boolPtr(value bool) *bool { return &value }
