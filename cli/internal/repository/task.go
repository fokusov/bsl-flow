package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	taskSchema      = 2
	maxTitle        = 200
	maxDescription  = 4000
	maxLabels       = 50
	maxLabelLength  = 64
	maxDependencies = 200
	maxProvenance   = 200
)

var allowedPriorities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
var allowedLifecycles = map[string]bool{"planned": true, "controller": true}
var provenanceFields = map[string]bool{"author": true, "source": true, "reference": true, "created_in": true}

// credentialPattern requires a value or credential-shaped header context. A
// plain title such as "Fix Authorization header handling" or "Проверить
// пароль" is therefore valid metadata, while actual values are rejected.
var secretPattern = regexp.MustCompile(`(?i)(?:\bauthorization\s*["']?\s*[:=]\s*["']?(?:bearer\s+)?[^\s,;}"']+|\bbearer\s+[A-Za-z0-9._~+/=-]{6,}|\b(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|private[_-]?key)\s*["']?\s*(?::|=|\bis\b)\s*["']?[^\s,;}"']+|пароль\s*[:=]\s*["']?[^\s,;}"']+|-----BEGIN [^-]*PRIVATE KEY-----|\bbasic\s+[A-Za-z0-9+/=]{8,})`)

var controlPattern = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// v1 controller state fields required by the closed state.schema.json. A v1
// journal without all of them is not a valid controller task.
var v1RequiredFields = []string{
	"schema_version", "task_id", "revision", "previous_sha256", "project_path",
	"worker_path", "baseline", "request_hash", "intent_hash", "policy_hash",
	"created_at", "updated_at", "request", "intent_revision",
	"authorization_revision", "correction_rounds", "policy_files", "attempts",
	"evidence", "events", "blockers", "acceptances", "policy_rules",
	"classification", "status", "stage", "active_attempt",
	"unresolved_effect", "question",
}

var v1StateFields = map[string]bool{
	"schema_version": true, "task_id": true, "revision": true, "previous_sha256": true,
	"project_path": true, "worker_path": true, "baseline": true, "request_hash": true,
	"intent_hash": true, "policy_hash": true, "created_at": true, "updated_at": true,
	"request": true, "intent_revision": true, "authorization_revision": true,
	"correction_rounds": true, "policy_files": true, "attempts": true, "evidence": true,
	"events": true, "blockers": true, "acceptances": true, "policy_rules": true,
	"classification": true, "status": true, "stage": true, "active_attempt": true,
	"unresolved_effect": true, "question": true, "repair": true,
}

var v1RequestFields = map[string]bool{
	"schema_version": true, "request_id": true, "prompt": true, "mode": true,
	"analysis_goal": true, "complexity": true, "risk": true, "impact_flags": true,
	"criteria": true, "provenance": true, "models": true, "execution_profile": true,
	"budget": true, "source_paths": true, "requirements": true, "require_spec_review": true,
	"require_code_review": true, "max_attempts": true, "max_source_repairs": true,
	"timeout_seconds": true,
}

var v1RequestRequiredFields = []string{
	"schema_version", "request_id", "prompt", "mode", "analysis_goal",
	"complexity", "risk", "impact_flags", "criteria", "provenance", "models",
}

var v2StateFields = map[string]bool{
	"schema_version": true, "task_id": true, "revision": true, "previous_sha256": true,
	"title": true, "description": true, "priority": true, "labels": true,
	"depends_on": true, "lifecycle": true, "archived": true, "created_at": true,
	"updated_at": true, "provenance": true, "controller": true, "migration": true,
}

var allowedV1Statuses = map[string]bool{
	"ready": true, "running": true, "needs_input": true, "blocked": true,
	"failed": true, "completed": true, "cancelled": true,
}

var allowedV1Stages = map[string]bool{
	"inspect": true, "spec": true, "spec_review": true, "implement": true,
	"code_review": true, "verify": true, "diagnose": true, "acceptance": true,
}

// Task is the parsed latest revision of a repository task journal.
type Task struct {
	ID          string
	Title       string
	Description string
	Priority    string
	Labels      []string
	DependsOn   []string
	Lifecycle   string
	Archived    bool
	CreatedAt   string
	UpdatedAt   string
	Provenance  map[string]any
	Revision    int64
	State       map[string]any
	// OriginWorktree is the resolved origin of the task: the saved provenance
	// reference when it points at an existing worktree of this clone, otherwise
	// the worktree that stored the journal.
	OriginWorktree string
}

func (r *Repository) taskDir(id string) (string, error) {
	if !isUUID(id) {
		return "", invalid("task id must be a lowercase UUID")
	}
	return filepath.Join(r.StorePath, "tasks", id), nil
}

func (r *Repository) readChain(directory string) ([]map[string]any, error) {
	safeDirectory, err := SafePath(directory)
	if err != nil {
		return nil, blocked("unsafe task journal path: %v", err)
	}
	revisionDirectory := filepath.Join(safeDirectory, "revisions")
	if _, err := SafePath(revisionDirectory); err != nil {
		return nil, blocked("unsafe revision journal path: %v", err)
	}
	entries, err := os.ReadDir(revisionDirectory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, blocked("cannot read revision journal: %v", err)
	}
	type revisionFile struct {
		name   string
		number int
	}
	var files []revisionFile
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, blocked("unexpected directory in revision journal: %s", entry.Name())
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		if len(name) != 11 || !strings.HasSuffix(name, ".json") {
			if strings.HasSuffix(name, ".json") {
				return nil, blocked("unexpected revision filename: %s", name)
			}
			continue
		}
		number := 0
		for index := 0; index < 6; index++ {
			if name[index] < '0' || name[index] > '9' {
				return nil, blocked("unexpected revision filename: %s", name)
			}
			number = number*10 + int(name[index]-'0')
		}
		files = append(files, revisionFile{name, number})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) == 0 {
		return nil, nil
	}
	var chain []map[string]any
	var previousHash any
	taskID := ""
	for index, file := range files {
		if file.number != index+1 {
			return nil, blocked("revision chain has a gap before %s", file.name)
		}
		data, err := ReadFileBytes(filepath.Join(revisionDirectory, file.name))
		if err != nil {
			return nil, blocked("corrupt revision %s: %v", file.name, err)
		}
		state, err := DecodeObject(data)
		if err != nil {
			return nil, blocked("corrupt revision %s: %v", file.name, err)
		}
		revision, ok := asInt(state["revision"])
		if !ok || revision != int64(file.number) {
			return nil, blocked("filename and revision disagree in %s", file.name)
		}
		currentID, ok := asString(state["task_id"])
		if !ok || currentID == "" {
			return nil, blocked("task_id is missing in %s", file.name)
		}
		if taskID == "" {
			taskID = currentID
		} else if currentID != taskID {
			return nil, blocked("task_id changed in %s", file.name)
		}
		if err := validateState(state, filepath.Base(directory)); err != nil {
			return nil, blocked("invalid state in %s: %v", file.name, err)
		}
		linked, present := state["previous_sha256"]
		if !present {
			return nil, blocked("previous_sha256 is missing in %s", file.name)
		}
		if index == 0 {
			if linked != nil {
				return nil, blocked("first revision must have null previous_sha256")
			}
		} else {
			linkedHash, ok := asString(linked)
			if !ok || !isSHA256(linkedHash) || linkedHash != previousHash {
				return nil, blocked("revision hash chain is broken at %s", file.name)
			}
		}
		hash, err := Hash(state)
		if err != nil {
			return nil, blocked("cannot hash revision %s: %v", file.name, err)
		}
		previousHash = hash
		chain = append(chain, state)
	}
	if err := r.validateAdoptionChain(directory, chain); err != nil {
		return nil, blocked("invalid adoption chain: %v", err)
	}
	return chain, nil
}

// validateV1State applies the closed v1 controller schema: every required
// controller field must exist with the documented type, and status/stage must
// be known enum values. Synthetic states without controller fields are
// rejected instead of entering the catalog as healthy tasks.
func validateV1State(state map[string]any) error {
	for field := range state {
		if !v1StateFields[field] {
			return fmt.Errorf("v1 state contains unsupported field %s", field)
		}
	}
	for _, field := range v1RequiredFields {
		if _, present := state[field]; !present {
			return fmt.Errorf("v1 controller state is missing required field %s", field)
		}
	}
	status, ok := asString(state["status"])
	if !ok || !allowedV1Statuses[status] {
		return errors.New("v1 status must be one of ready|running|needs_input|blocked|failed|completed|cancelled")
	}
	if stage, ok := asString(state["stage"]); !ok || !allowedV1Stages[stage] {
		return errors.New("v1 stage must be one of inspect|spec|spec_review|implement|code_review|verify|diagnose|acceptance")
	}
	for _, field := range []string{"attempts", "evidence", "events", "blockers", "acceptances", "policy_files"} {
		if _, ok := state[field].([]any); !ok {
			return fmt.Errorf("v1 %s must be an array", field)
		}
	}
	for _, field := range []string{"policy_rules", "classification"} {
		if _, ok := state[field].(map[string]any); !ok {
			return fmt.Errorf("v1 %s must be an object", field)
		}
	}
	if _, ok := asString(state["project_path"]); !ok || state["project_path"].(string) == "" {
		return errors.New("v1 project_path must be a non-empty string")
	}
	if _, ok := asString(state["worker_path"]); !ok || state["worker_path"].(string) == "" {
		return errors.New("v1 worker_path must be a non-empty string")
	}
	if _, ok := asString(state["baseline"]); !ok || state["baseline"].(string) == "" {
		return errors.New("v1 baseline must be a non-empty string")
	}
	request, ok := state["request"].(map[string]any)
	if !ok {
		return errors.New("v1 request must be an object")
	}
	if err := validateV1Request(request); err != nil {
		return err
	}
	for _, field := range []string{"active_attempt"} {
		switch state[field].(type) {
		case nil, string:
		default:
			return fmt.Errorf("v1 %s must be a string or null", field)
		}
	}
	for _, field := range []string{"unresolved_effect", "question"} {
		switch state[field].(type) {
		case nil, map[string]any:
		default:
			return fmt.Errorf("v1 %s must be an object or null", field)
		}
	}
	for _, field := range []string{"intent_revision", "authorization_revision", "correction_rounds"} {
		if value, ok := asInt(state[field]); !ok || value < 0 {
			return fmt.Errorf("v1 %s must be a non-negative integer", field)
		}
	}
	if linked, present := state["previous_sha256"]; present {
		switch linked.(type) {
		case nil, string:
		default:
			return errors.New("v1 previous_sha256 must be a string or null")
		}
	}
	if _, err := Hash(state); err != nil {
		return fmt.Errorf("v1 state is not canonically encodable: %v", err)
	}
	for _, field := range []string{"created_at", "updated_at"} {
		value, ok := asString(state[field])
		if err := validateTimestamp(value, ok, "v1 "+field); err != nil {
			return err
		}
	}
	for _, field := range []string{"request_hash", "intent_hash", "policy_hash"} {
		value, ok := asString(state[field])
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("v1 %s must be a non-empty string", field)
		}
	}
	return nil
}

func validateV1Request(request map[string]any) error {
	if len(request) == 0 {
		return errors.New("v1 request must not be empty")
	}
	for field := range request {
		if !v1RequestFields[field] {
			return errors.New(safeErrorMessage(fmt.Sprintf("v1 request contains unsupported field %s", field)))
		}
	}
	for _, field := range v1RequestRequiredFields {
		if _, present := request[field]; !present {
			return fmt.Errorf("v1 request.%s is required", field)
		}
	}
	if version, ok := asInt(request["schema_version"]); !ok || version != 1 {
		return errors.New("v1 request.schema_version must be 1")
	}
	requestID, ok := asString(request["request_id"])
	if !ok || !isUUID(requestID) {
		return errors.New("v1 request.request_id must be a lowercase UUID")
	}
	if prompt, ok := asString(request["prompt"]); !ok || prompt == "" {
		return errors.New("v1 request.prompt must be a non-empty string")
	}
	mode, ok := asString(request["mode"])
	if !ok || (mode != "analysis_only" && mode != "implement") {
		return errors.New("v1 request.mode is invalid")
	}
	analysisGoal, ok := asString(request["analysis_goal"])
	if !ok || (analysisGoal != "analysis" && analysisGoal != "specification") {
		return errors.New("v1 request.analysis_goal is invalid")
	}
	complexity, ok := asString(request["complexity"])
	if !ok || (complexity != "S" && complexity != "M" && complexity != "L") {
		return errors.New("v1 request.complexity is invalid")
	}
	risk, ok := asString(request["risk"])
	if !ok || (risk != "low" && risk != "medium" && risk != "high") {
		return errors.New("v1 request.risk is invalid")
	}
	if err := validateV1ImpactFlags(request["impact_flags"]); err != nil {
		return err
	}
	if err := validateV1CriteriaShape(request["criteria"]); err != nil {
		return err
	}
	if err := validateV1ProvenanceShape(request["provenance"]); err != nil {
		return err
	}
	if err := validateV1ModelsShape(request["models"]); err != nil {
		return err
	}
	if sourcePaths, present := request["source_paths"]; present {
		if err := validateV1StringArrayShape(sourcePaths, "v1 request.source_paths"); err != nil {
			return err
		}
	}
	if requirements, present := request["requirements"]; present {
		if err := validateV1ArrayShape(requirements, "v1 request.requirements"); err != nil {
			return err
		}
	}
	for _, field := range []string{"require_spec_review", "require_code_review"} {
		if value, present := request[field]; present {
			if _, ok := asBool(value); !ok {
				return fmt.Errorf("v1 request.%s must be a boolean", field)
			}
		}
	}
	for _, field := range []string{"execution_profile", "budget"} {
		if value, present := request[field]; present {
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("v1 request.%s must be an object", field)
			}
		}
	}
	for _, field := range []string{"max_attempts", "timeout_seconds", "max_source_repairs"} {
		if value, present := request[field]; present {
			if _, ok := asInt(value); !ok {
				return fmt.Errorf("v1 request.%s must be an integer", field)
			}
		}
	}
	return nil
}

var v1ImpactFlags = map[string]bool{
	"permissions": true, "data_migration": true, "data_deletion": true,
	"posting": true, "data_exchange": true, "form_flow": true,
	"external_artifact": true, "ambiguous_business_rule": true,
}

var v1CriterionFields = map[string]bool{
	"id": true, "observation": true, "kind": true, "path": true, "contains": true,
	"executable": true, "arguments": true, "report": true, "expected_tests": true,
	"target": true, "profile": true, "native_1c": true, "retry_safe": true,
	"protected_paths": true,
}

var v1CriterionKinds = map[string]bool{
	"file_assertion": true, "static": true, "unit": true, "integration": true,
	"ui": true, "external_artifact": true,
}

var v1SafeIDPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")

func validateV1ImpactFlags(value any) error {
	items := anyItems(value)
	if items == nil {
		return errors.New("v1 request.impact_flags must be an array")
	}
	seen := map[string]bool{}
	for _, item := range items {
		flag, ok := asString(item)
		if !ok || !v1ImpactFlags[flag] {
			return errors.New("v1 request.impact_flags contains an unsupported value")
		}
		if seen[flag] {
			return errors.New("v1 request.impact_flags must contain unique values")
		}
		seen[flag] = true
	}
	return nil
}

func validateV1StringArrayShape(value any, name string) error {
	items := anyItems(value)
	if items == nil {
		return fmt.Errorf("%s must be an array", name)
	}
	for _, item := range items {
		if _, ok := asString(item); !ok {
			return fmt.Errorf("%s must contain strings", name)
		}
	}
	return nil
}

func validateV1ArrayShape(value any, name string) error {
	if anyItems(value) == nil {
		return fmt.Errorf("%s must be an array", name)
	}
	return nil
}

func validateV1CriteriaShape(value any) error {
	items := anyItems(value)
	if items == nil {
		return errors.New("v1 request.criteria must be an array")
	}
	seen := map[string]bool{}
	for _, raw := range items {
		criterion, ok := raw.(map[string]any)
		if !ok {
			return errors.New("v1 criterion must be an object")
		}
		for field := range criterion {
			if !v1CriterionFields[field] {
				return errors.New(safeErrorMessage(fmt.Sprintf("v1 criterion contains unsupported field %s", field)))
			}
		}
		for _, field := range []string{"id", "observation", "kind"} {
			if _, present := criterion[field]; !present {
				return fmt.Errorf("v1 criterion.%s is required", field)
			}
		}
		id, ok := asString(criterion["id"])
		if !ok || !v1SafeIDPattern.MatchString(id) || seen[id] {
			return errors.New("v1 criterion ids must be safe and unique")
		}
		if _, ok := asString(criterion["observation"]); !ok {
			return errors.New("v1 criterion.observation must be a string")
		}
		kind, ok := asString(criterion["kind"])
		if !ok || !v1CriterionKinds[kind] {
			return errors.New("v1 criterion.kind is invalid")
		}
		for _, field := range []string{"path", "contains", "executable", "report", "target"} {
			if rawValue, present := criterion[field]; present {
				if _, ok := asString(rawValue); !ok {
					return fmt.Errorf("v1 criterion.%s must be a string", field)
				}
			}
		}
		for _, field := range []string{"arguments", "expected_tests", "protected_paths"} {
			if rawValue, present := criterion[field]; present {
				if err := validateV1StringArrayShape(rawValue, "v1 criterion."+field); err != nil {
					return err
				}
			}
		}
		if profile, present := criterion["profile"]; present {
			if _, ok := profile.(map[string]any); !ok {
				return errors.New("v1 criterion.profile must be an object")
			}
		}
		if native, present := criterion["native_1c"]; present {
			if _, ok := native.(map[string]any); !ok {
				return errors.New("v1 criterion.native_1c must be an object")
			}
		}
		if retry, present := criterion["retry_safe"]; present {
			if _, ok := asBool(retry); !ok {
				return errors.New("v1 criterion.retry_safe must be a boolean")
			}
		}
		seen[id] = true
	}
	return nil
}

func validateV1ProvenanceShape(value any) error {
	provenance, ok := value.(map[string]any)
	if !ok {
		return errors.New("v1 request.provenance must be an object")
	}
	if len(provenance) != 3 {
		return errors.New("v1 request.provenance contains unsupported fields")
	}
	for _, field := range []string{"source", "reference", "text"} {
		if _, present := provenance[field]; !present {
			return fmt.Errorf("v1 request.provenance.%s is required", field)
		}
	}
	if source, ok := asString(provenance["source"]); !ok || source != "user" {
		return errors.New("v1 request.provenance.source must be user")
	}
	for _, field := range []string{"reference", "text"} {
		if _, ok := asString(provenance[field]); !ok {
			return fmt.Errorf("v1 request.provenance.%s must be a string", field)
		}
	}
	return nil
}

func validateV1ModelsShape(value any) error {
	models, ok := value.(map[string]any)
	if !ok {
		return errors.New("v1 request.models must be an object")
	}
	if len(models) != 4 {
		return errors.New("v1 request.models contains unsupported fields")
	}
	for _, field := range []string{"worker", "worker_effort", "reviewer", "reviewer_effort"} {
		if _, present := models[field]; !present {
			return fmt.Errorf("v1 request.models.%s is required", field)
		}
	}
	for _, field := range []string{"worker", "reviewer"} {
		if _, ok := asString(models[field]); !ok {
			return fmt.Errorf("v1 request.models.%s must be a string", field)
		}
	}
	for _, field := range []string{"worker_effort", "reviewer_effort"} {
		if value := models[field]; value != nil {
			if _, ok := asString(value); !ok {
				return fmt.Errorf("v1 request.models.%s must be a string or null", field)
			}
		}
	}
	return nil
}

func validateTimestamp(value string, present bool, name string) error {
	if !present || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty RFC3339Nano timestamp", name)
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s must be an RFC3339Nano timestamp", name)
	}
	return nil
}

func validateState(state map[string]any, directoryID string) error {
	taskID, ok := asString(state["task_id"])
	if !ok || taskID != directoryID {
		return errors.New("task_id does not match the task directory")
	}
	version, ok := asInt(state["schema_version"])
	if !ok {
		return errors.New("schema_version is missing")
	}
	switch version {
	case 1:
		return validateV1State(state)
	case 2:
		for field := range state {
			if !v2StateFields[field] {
				return fmt.Errorf("v2 state contains unsupported field %s", field)
			}
		}
		if revision, ok := asInt(state["revision"]); !ok || revision < 1 {
			return errors.New("revision must be a positive integer")
		}
		for _, field := range []string{"labels", "depends_on"} {
			if _, present := state[field]; !present {
				return fmt.Errorf("%s is missing", field)
			}
		}
		title, ok := asString(state["title"])
		if !ok || strings.TrimSpace(title) == "" {
			return errors.New("title must be a non-empty string")
		}
		if controlPattern.MatchString(title) || secretPattern.MatchString(title) {
			return errors.New("title must not contain credentials or control characters")
		}
		if description, present := state["description"]; present {
			text, ok := description.(string)
			if !ok {
				return errors.New("description must be a string")
			}
			if controlPattern.MatchString(text) || secretPattern.MatchString(text) {
				return errors.New("description must not contain credentials or control characters")
			}
		}
		priority, ok := asString(state["priority"])
		if !ok || !allowedPriorities[priority] {
			return errors.New("priority is invalid")
		}
		labels, err := normalizeLabels(state["labels"])
		if err != nil {
			return err
		}
		for _, label := range labels {
			if controlPattern.MatchString(label) || secretPattern.MatchString(label) {
				return errors.New("labels must not contain credentials or control characters")
			}
		}
		lifecycle, ok := asString(state["lifecycle"])
		if !ok || !allowedLifecycles[lifecycle] {
			return errors.New("lifecycle is invalid")
		}
		if lifecycle == "planned" {
			if _, present := state["controller"]; present {
				return errors.New("planned task must not contain controller state")
			}
		} else {
			if migration, present := state["migration"]; present {
				if err := validateMigrationMetadata(migration); err != nil {
					return err
				}
			}
			if err := validateControllerPayload(state["controller"], directoryID, asBoolOr(asMap(state["migration"])["requires_rebind"])); err != nil {
				return err
			}
		}
		if _, err := normalizeDependencies(state["depends_on"]); err != nil {
			return err
		}
		if _, ok := asBool(state["archived"]); !ok {
			return errors.New("archived must be a boolean")
		}
		createdAt, ok := asString(state["created_at"])
		if err := validateTimestamp(createdAt, ok, "created_at"); err != nil {
			return err
		}
		updatedAt, ok := asString(state["updated_at"])
		if err := validateTimestamp(updatedAt, ok, "updated_at"); err != nil {
			return err
		}
		if _, err := normalizeProvenance(state["provenance"]); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported task schema version %d", version)
	}
}

// ReadTask returns the latest revision of a task or an invalid error when it is
// absent.
func (r *Repository) ReadTask(id string) (*Task, error) {
	directory, err := r.taskDir(id)
	if err != nil {
		return nil, err
	}
	chain, err := r.readChain(directory)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		if _, statErr := os.Lstat(directory); statErr == nil {
			return nil, blocked("task journal is orphaned: no revisions for %s", id)
		} else if !os.IsNotExist(statErr) {
			return nil, blocked("cannot inspect task journal: %v", statErr)
		}
		return nil, invalid("task not found: %s", id)
	}
	return taskFromState(chain[len(chain)-1])
}

func taskFromState(state map[string]any) (*Task, error) {
	revision, ok := asInt(state["revision"])
	if !ok {
		return nil, blocked("revision is missing")
	}
	id, _ := asString(state["task_id"])
	title, _ := asString(state["title"])
	description, _ := asString(state["description"])
	priority, _ := asString(state["priority"])
	if priority == "" {
		priority = "low"
	}
	labels, _ := asStringSlice(state["labels"])
	dependencies, _ := asStringSlice(state["depends_on"])
	lifecycle, _ := asString(state["lifecycle"])
	if lifecycle == "" {
		lifecycle = "planned"
	}
	archived, _ := asBool(state["archived"])
	created, _ := asString(state["created_at"])
	updated, _ := asString(state["updated_at"])
	provenance, _ := state["provenance"].(map[string]any)
	origin, _ := asString(provenance["created_in"])
	return &Task{ID: id, Title: title, Description: description, Priority: priority, Labels: labels, DependsOn: dependencies, Lifecycle: lifecycle, Archived: archived, CreatedAt: created, UpdatedAt: updated, Provenance: provenance, Revision: revision, State: state, OriginWorktree: origin}, nil
}

func (r *Repository) writeRevision(id string, card map[string]any, expected int64) (map[string]any, error) {
	directory, err := r.taskDir(id)
	if err != nil {
		return nil, err
	}
	_, directoryStatErr := os.Lstat(directory)
	directoryExisted := directoryStatErr == nil
	if directoryStatErr != nil && !os.IsNotExist(directoryStatErr) {
		return nil, blocked("cannot inspect task journal: %v", directoryStatErr)
	}
	unlock, err := Lock(filepath.Join(directory, ".writer.lock"))
	if err != nil {
		return nil, conflict("task journal is locked: %v", err)
	}
	defer unlock()
	chain, err := r.readChain(directory)
	if err != nil {
		return nil, err
	}
	actual := int64(len(chain))
	if actual == 0 && directoryExisted {
		return nil, blocked("task journal is orphaned and cannot be reused: %s", id)
	}
	if actual != expected {
		return nil, conflict("expected revision %d, actual revision %d", expected, actual)
	}
	if actual >= 999999 {
		return nil, blocked("revision journal reached its supported limit")
	}
	state := make(map[string]any, len(card)+3)
	for key, value := range card {
		state[key] = value
	}
	state["schema_version"] = taskSchema
	state["task_id"] = id
	state["revision"] = actual + 1
	if actual == 0 {
		state["previous_sha256"] = nil
	} else {
		hash, err := Hash(chain[actual-1])
		if err != nil {
			return nil, blocked("cannot hash previous revision: %v", err)
		}
		state["previous_sha256"] = hash
	}
	if err := validateState(state, id); err != nil {
		return nil, invalid("invalid task state: %v", err)
	}
	data, err := Canonical(state)
	if err != nil {
		return nil, invalid("invalid task state: %v", err)
	}
	revisionPath := filepath.Join(directory, "revisions", fmt.Sprintf("%06d.json", actual+1))
	if err := AtomicWrite(revisionPath, data, false); err != nil {
		return nil, conflict("cannot publish revision: %v", err)
	}
	persisted, err := r.readChain(directory)
	if err != nil {
		return nil, err
	}
	latest := persisted[len(persisted)-1]
	hash, err := Hash(latest)
	if err != nil {
		return nil, blocked("cannot hash published revision: %v", err)
	}
	current := map[string]any{"revision": latest["revision"], "sha256": hash}
	currentData, err := Canonical(current)
	if err != nil {
		return nil, err
	}
	if err := AtomicWrite(filepath.Join(directory, "current.json"), currentData, true); err != nil {
		return nil, blocked("revision published but current projection failed: %v", err)
	}
	return latest, nil
}

func normalizePriority(value any) (string, error) {
	if value == nil {
		return "low", nil
	}
	text, ok := asString(value)
	if !ok || !allowedPriorities[text] {
		return "", invalid("priority must be one of low|medium|high|critical")
	}
	return text, nil
}

func normalizeLabels(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	labels, ok := asStringSlice(value)
	if !ok {
		return nil, invalid("labels must be an array of strings")
	}
	if len(labels) > maxLabels {
		return nil, invalid("labels must not contain more than %d entries", maxLabels)
	}
	seen := map[string]bool{}
	for _, label := range labels {
		if label == "" || len(label) > maxLabelLength {
			return nil, invalid("label must be a non-empty string of at most %d characters", maxLabelLength)
		}
		if seen[label] {
			return nil, invalid("duplicate label")
		}
		if controlPattern.MatchString(label) || secretPattern.MatchString(label) || sensitiveLabel(label) {
			return nil, invalid("label must not contain credentials or control characters")
		}
		seen[label] = true
	}
	return labels, nil
}

func normalizeDependencies(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	dependencies, ok := asStringSlice(value)
	if !ok {
		return nil, invalid("depends_on must be an array of UUIDs")
	}
	if len(dependencies) > maxDependencies {
		return nil, invalid("depends_on must not contain more than %d entries", maxDependencies)
	}
	seen := map[string]bool{}
	for _, dependency := range dependencies {
		if !isUUID(dependency) {
			return nil, invalid("depends_on entry must be a lowercase UUID: %s", dependency)
		}
		if seen[dependency] {
			return nil, invalid("duplicate dependency: %s", dependency)
		}
		seen[dependency] = true
	}
	return dependencies, nil
}

func normalizeProvenance(value any) (map[string]any, error) {
	result := map[string]any{}
	if value == nil {
		return result, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("provenance must be an object")
	}
	for key, raw := range object {
		if !provenanceFields[key] {
			return nil, invalid("unsupported provenance field: %s", key)
		}
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" || len(text) > maxProvenance {
			return nil, invalid("provenance.%s must be a non-empty string of at most %d characters", key, maxProvenance)
		}
		if controlPattern.MatchString(text) || secretPattern.MatchString(text) {
			return nil, invalid("provenance.%s must not contain credentials or control characters", key)
		}
		result[key] = text
	}
	return result, nil
}

func validateText(name string, value any, maximum int) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := asString(value)
	if !ok {
		return "", invalid("%s must be a string", name)
	}
	if len(text) > maximum {
		return "", invalid("%s must be at most %d characters", name, maximum)
	}
	if controlPattern.MatchString(text) || secretPattern.MatchString(text) {
		return "", invalid("%s must not contain credentials or control characters", name)
	}
	return text, nil
}

// plannedCard validates a create input and returns the first planned card.
func plannedCard(input map[string]any, worktree string) (map[string]any, error) {
	allowed := map[string]bool{"schema_version": true, "title": true, "description": true, "priority": true, "labels": true, "depends_on": true, "provenance": true}
	for key := range input {
		if !allowed[key] {
			return nil, invalid("unsupported create field: %s", key)
		}
	}
	if version, present := input["schema_version"]; present {
		number, ok := asInt(version)
		if !ok || number != 1 {
			return nil, invalid("schema_version must be 1")
		}
	} else {
		return nil, invalid("schema_version is required")
	}
	title, err := validateText("title", input["title"], maxTitle)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, invalid("title must not be empty")
	}
	description, err := validateText("description", input["description"], maxDescription)
	if err != nil {
		return nil, err
	}
	priority, err := normalizePriority(input["priority"])
	if err != nil {
		return nil, err
	}
	labels, err := normalizeLabels(input["labels"])
	if err != nil {
		return nil, err
	}
	dependencies, err := normalizeDependencies(input["depends_on"])
	if err != nil {
		return nil, err
	}
	provenance, err := normalizeProvenance(input["provenance"])
	if err != nil {
		return nil, err
	}
	if existing, present := provenance["created_in"]; !present || existing == "" {
		provenance["created_in"] = worktree
	}
	now := nowUTC()
	return map[string]any{
		"title":       title,
		"description": description,
		"priority":    priority,
		"labels":      toAnySlice(labels),
		"depends_on":  toAnySlice(dependencies),
		"lifecycle":   "planned",
		"archived":    false,
		"created_at":  now,
		"updated_at":  now,
		"provenance":  provenance,
	}, nil
}

// patchCard applies an edit patch to an existing card.
func patchCard(current map[string]any, patch map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"title": true, "description": true, "priority": true, "labels": true, "depends_on": true}
	if len(patch) == 0 {
		return nil, invalid("edit input must contain at least one field")
	}
	for key := range patch {
		if !allowed[key] {
			return nil, invalid("unsupported edit field: %s", key)
		}
	}
	card := make(map[string]any, len(current))
	for key, value := range current {
		card[key] = value
	}
	if raw, present := patch["title"]; present {
		title, err := validateText("title", raw, maxTitle)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(title) == "" {
			return nil, invalid("title must not be empty")
		}
		card["title"] = title
	}
	if raw, present := patch["description"]; present {
		description, err := validateText("description", raw, maxDescription)
		if err != nil {
			return nil, err
		}
		card["description"] = description
	}
	if raw, present := patch["priority"]; present {
		priority, err := normalizePriority(raw)
		if err != nil {
			return nil, err
		}
		card["priority"] = priority
	}
	if raw, present := patch["labels"]; present {
		labels, err := normalizeLabels(raw)
		if err != nil {
			return nil, err
		}
		card["labels"] = toAnySlice(labels)
	}
	if raw, present := patch["depends_on"]; present {
		dependencies, err := normalizeDependencies(raw)
		if err != nil {
			return nil, err
		}
		card["depends_on"] = toAnySlice(dependencies)
	}
	card["updated_at"] = nowUTC()
	return card, nil
}
