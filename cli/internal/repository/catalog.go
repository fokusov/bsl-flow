package repository

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Row is the safe projection of a task returned by list/overview. It carries
// no prompt, raw evidence payload or credentials: evidence references are
// attempt IDs only.
type Row struct {
	TaskID          string   `json:"task_id"`
	Title           string   `json:"title"`
	Lifecycle       string   `json:"lifecycle"`
	Status          string   `json:"status"`
	Stage           any      `json:"stage"`
	NextAction      any      `json:"next_action"`
	Question        string   `json:"question"`
	Blockers        []string `json:"blockers"`
	AttemptCount    int      `json:"attempt_count"`
	AcceptanceCount int      `json:"acceptance_count"`
	EvidenceRefs    []string `json:"evidence_refs"`
	Priority        string   `json:"priority"`
	Labels          []string `json:"labels"`
	DependsOn       []string `json:"depends_on"`
	DependencyCount int      `json:"dependency_count"`
	Revision        int64    `json:"revision"`
	SchemaVersion   int      `json:"schema_version"`
	Archived        bool     `json:"archived"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	OriginWorktree  string   `json:"origin_worktree"`
	Source          string   `json:"source"`
	Health          string   `json:"health"`
	Stale           bool     `json:"stale"`
}

// Filters constrain a list or overview query.
type Filters struct {
	Status        string
	Stage         string
	Priority      string
	Label         string
	Archived      *bool
	UpdatedBefore string
	UpdatedAfter  string
	Limit         int
	Cursor        string
}

func rowFromTask(task *Task, source string) Row {
	status := task.Lifecycle
	if status == "" {
		status = "planned"
	}
	return Row{
		TaskID:          task.ID,
		Title:           task.Title,
		Lifecycle:       task.Lifecycle,
		Status:          status,
		Stage:           nil,
		NextAction:      nil,
		Question:        "",
		Blockers:        []string{},
		EvidenceRefs:    []string{},
		Priority:        task.Priority,
		Labels:          append([]string{}, task.Labels...),
		DependsOn:       append([]string{}, task.DependsOn...),
		DependencyCount: len(task.DependsOn),
		Revision:        task.Revision,
		SchemaVersion:   taskSchema,
		Archived:        task.Archived,
		CreatedAt:       task.CreatedAt,
		UpdatedAt:       task.UpdatedAt,
		OriginWorktree:  task.OriginWorktree,
		Source:          source,
		Health:          "ok",
	}
}

// Catalog derives every valid task from the authoritative journals. A corrupt
// task never hides the others; it becomes a diagnostic entry.
func (r *Repository) Catalog() ([]Row, []map[string]any, error) {
	rows := []Row{}
	diagnostics := []map[string]any{}
	tasksDirectory := filepath.Join(r.StorePath, "tasks")
	if _, err := SafePath(tasksDirectory); err != nil {
		return nil, nil, blocked("unsafe task store path: %v", err)
	}
	entries, err := os.ReadDir(tasksDirectory)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, blocked("cannot enumerate tasks: %v", err)
	}
	known := map[string]string{}
	canonicalIDs := map[string]bool{}
	for _, entry := range entries {
		id := entry.Name()
		if !isUUID(id) {
			if entry.IsDir() {
				diagnostics = append(diagnostics, diagnostic(id, "repository", "corrupt", "unexpected task directory name"))
			}
			continue
		}
		// Reserve every UUID directory before validating its chain. A corrupt or
		// orphaned canonical journal must never be replaced by a healthy legacy
		// copy with the same ID.
		canonicalIDs[id] = true
		chain, err := r.readChain(filepath.Join(tasksDirectory, id))
		if err != nil {
			diagnostics = append(diagnostics, diagnostic(id, "repository", "corrupt", err.Error()))
			continue
		}
		if len(chain) == 0 {
			diagnostics = append(diagnostics, diagnostic(id, "repository", "orphaned", "task directory has no revisions"))
			continue
		}
		task, err := taskFromState(chain[len(chain)-1])
		if err != nil {
			diagnostics = append(diagnostics, diagnostic(id, "repository", "corrupt", err.Error()))
			continue
		}
		row := rowFromTask(task, "repository")
		// Origin is the saved creation provenance when it still resolves to an
		// existing worktree of this clone; otherwise the storing worktree.
		row.OriginWorktree = r.resolveOrigin(task.OriginWorktree)
		rows = append(rows, row)
		if hash, err := Hash(chain[len(chain)-1]); err == nil {
			known[id] = hash
		}
	}
	legacyRows, legacyDiagnostics := r.legacyCatalog(known, canonicalIDs)
	rows = append(rows, legacyRows...)
	diagnostics = append(diagnostics, legacyDiagnostics...)
	sort.SliceStable(rows, func(i, j int) bool {
		if compared, ok := compareTimestamps(rows[i].UpdatedAt, rows[j].UpdatedAt); ok {
			if compared != 0 {
				return compared > 0
			}
		} else if rows[i].UpdatedAt != rows[j].UpdatedAt {
			// Catalog rows are validated before they reach this comparator. Keep
			// a deterministic fallback for callers constructing a Row directly.
			return rows[i].UpdatedAt > rows[j].UpdatedAt
		}
		return rows[i].TaskID < rows[j].TaskID
	})
	return rows, diagnostics, nil
}

func diagnostic(id, source, health, detail string) map[string]any {
	return map[string]any{"task_id": id, "source": source, "health": health, "detail": detail}
}

func applyFilters(rows []Row, filters Filters, cloneID string) ([]Row, string, error) {
	updatedBefore, hasUpdatedBefore, err := parseFilterTimestamp(filters.UpdatedBefore, "--updated-before")
	if err != nil {
		return nil, "", err
	}
	updatedAfter, hasUpdatedAfter, err := parseFilterTimestamp(filters.UpdatedAfter, "--updated-after")
	if err != nil {
		return nil, "", err
	}
	filtered := make([]Row, 0, len(rows))
	for _, row := range rows {
		if filters.Status != "" && row.Status != filters.Status {
			continue
		}
		if filters.Stage != "" {
			stage, _ := row.Stage.(string)
			if stage != filters.Stage {
				continue
			}
		}
		if filters.Priority != "" && row.Priority != filters.Priority {
			continue
		}
		if filters.Label != "" && !contains(row.Labels, filters.Label) {
			continue
		}
		if filters.Archived != nil && row.Archived != *filters.Archived {
			continue
		}
		if hasUpdatedBefore || hasUpdatedAfter {
			updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
			if err != nil {
				continue
			}
			if hasUpdatedBefore && !updatedAt.Before(updatedBefore) {
				continue
			}
			if hasUpdatedAfter && !updatedAt.After(updatedAfter) {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	signature := filterSignature(filters)
	start := 0
	if filters.Cursor != "" {
		cursor, err := decodeCursor(filters.Cursor)
		if err != nil {
			return nil, "", invalid("invalid cursor")
		}
		if cursor.Repository != cloneID {
			return nil, "", invalid("cursor belongs to a different repository")
		}
		if cursor.Signature != signature {
			return nil, "", invalid("cursor does not match the current filters")
		}
		found := false
		for index, row := range filtered {
			if cursorKey(row) == cursor.Key {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", invalid("cursor does not match the current result set")
		}
	}
	end := len(filtered)
	next := ""
	if filters.Limit > 0 && start+filters.Limit < end {
		end = start + filters.Limit
		next = encodeCursor(cloneID, signature, cursorKey(filtered[end-1]))
	}
	if start > len(filtered) {
		start = len(filtered)
	}
	return filtered[start:end], next, nil
}

func parseFilterTimestamp(value, name string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, invalid("%s must be an RFC3339Nano timestamp", name)
	}
	return parsed, true, nil
}

func compareTimestamps(left, right string) (int, bool) {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	if leftErr != nil || rightErr != nil {
		return 0, false
	}
	if leftTime.After(rightTime) {
		return 1, true
	}
	if leftTime.Before(rightTime) {
		return -1, true
	}
	return 0, true
}

func cursorKey(row Row) string {
	return row.UpdatedAt + "|" + row.TaskID
}

type cursorPayload struct {
	Repository string
	Signature  string
	Key        string
}

func filterSignature(filters Filters) string {
	archived := "any"
	if filters.Archived != nil {
		archived = strconv.FormatBool(*filters.Archived)
	}
	value := map[string]any{
		"status":         filters.Status,
		"stage":          filters.Stage,
		"priority":       filters.Priority,
		"label":          filters.Label,
		"archived":       archived,
		"updated_before": filters.UpdatedBefore,
		"updated_after":  filters.UpdatedAfter,
	}
	hash, err := Hash(value)
	if err != nil {
		return ""
	}
	return hash
}

func encodeCursor(cloneID, signature, key string) string {
	data, _ := json.Marshal(map[string]any{"v": 1, "r": cloneID, "f": signature, "k": key})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(value string) (cursorPayload, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorPayload{}, err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return cursorPayload{}, err
	}
	version, _ := object["v"].(float64)
	repository, _ := object["r"].(string)
	signature, _ := object["f"].(string)
	key, _ := object["k"].(string)
	if version != 1 || repository == "" || signature == "" || key == "" {
		return cursorPayload{}, fmt.Errorf("malformed cursor")
	}
	return cursorPayload{Repository: repository, Signature: signature, Key: key}, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// History returns a deterministic timeline built from verified revisions.
func (r *Repository) History(id string) ([]map[string]any, error) {
	directory, err := r.taskDir(id)
	if err != nil {
		return nil, err
	}
	directoryExists := false
	if _, statErr := os.Lstat(directory); statErr == nil {
		directoryExists = true
	} else if !os.IsNotExist(statErr) {
		return nil, blocked("cannot inspect task journal: %v", statErr)
	}
	chain, err := r.readChain(directory)
	if err != nil {
		return nil, err
	}
	source := "repository"
	origin := r.Worktree
	if len(chain) == 0 {
		if directoryExists {
			return nil, blocked("task journal is orphaned: no revisions for %s", id)
		}
		resolved, _, err := r.resolveLegacy(id)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, invalid("task not found: %s", id)
		}
		chain = resolved.chain
		source = "legacy"
		origin = resolved.origin
	} else {
		// Keep canonical history authoritative for catalog merge, but do not let
		// a targeted history read hide a corrupt or divergent same-ID legacy
		// journal discovered in another worktree.
		if legacyErr := r.checkLegacyConflict(id, chain[len(chain)-1]); legacyErr != nil {
			return nil, legacyErr
		}
	}
	if len(chain) == 0 {
		return nil, blocked("task journal has no revisions: %s", id)
	}
	events := make([]map[string]any, 0, len(chain))
	for index, state := range chain {
		revision, _ := asInt(state["revision"])
		hash, _ := Hash(state)
		projectionRow := Row{TaskID: id, Source: source, OriginWorktree: origin}
		if source == "legacy" {
			projectionRow, _ = legacyRow(id, state)
			projectionRow.OriginWorktree = origin
		} else if task, taskErr := taskFromState(state); taskErr == nil {
			projectionRow = rowFromTask(task, source)
			projectionRow.OriginWorktree = r.resolveOrigin(task.OriginWorktree)
		}
		event := map[string]any{"revision": revision, "at": state["updated_at"], "revision_sha256": hash, "source": source, "controller": controllerProjection(state, projectionRow)}
		if previous, present := state["previous_sha256"]; present {
			event["previous_sha256"] = previous
		}
		if index == 0 {
			event["event"] = "created"
		} else {
			event["event"] = "updated"
			previous := chain[index-1]
			watched := []string{"title", "description", "priority", "labels", "depends_on", "lifecycle", "archived", "controller"}
			if source == "legacy" {
				watched = []string{"request", "intent_revision", "authorization_revision", "attempts", "evidence", "events", "blockers", "acceptances", "status", "stage", "active_attempt", "unresolved_effect", "question"}
			}
			changes := []any{}
			for _, field := range watched {
				if !equalJSON(previous[field], state[field]) {
					changes = append(changes, field)
				}
			}
			if source == "repository" {
				// For repository tasks limit the watched set to the card fields. The
				// nested controller is a single closed field; its raw request/evidence
				// remains out of this public event projection.
				filtered := []any{}
				for _, field := range changes {
					if name, ok := field.(string); ok && name != "status" && name != "stage" {
						filtered = append(filtered, name)
					}
				}
				changes = filtered
			}
			if previousStatus, _ := asString(previous["status"]); previousStatus != "" {
				if status, _ := asString(state["status"]); status != previousStatus {
					event["status_change"] = map[string]any{"from": previousStatus, "to": state["status"]}
				}
			}
			event["fields"] = changes
		}
		events = append(events, event)
	}
	return events, nil
}

func equalJSON(left, right any) bool {
	leftData, leftErr := Canonical(left)
	rightData, rightErr := Canonical(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftData) == string(rightData)
}

// Overview aggregates lifecycle counters over the current catalog.
func Overview(rows []Row, diagnostics []map[string]any) map[string]any {
	byStatus := map[string]int{}
	byStage := map[string]int{}
	byPriority := map[string]int{}
	archived := 0
	index := map[string]Row{}
	for _, row := range rows {
		byStatus[row.Status]++
		if row.Priority != "" {
			byPriority[row.Priority]++
		}
		if stage, ok := row.Stage.(string); ok && stage != "" {
			byStage[stage]++
		}
		if row.Archived {
			archived++
		}
		index[row.TaskID] = row
	}
	dependencyBlocked := 0
	for _, row := range rows {
		if row.Status != "planned" {
			continue
		}
		for _, dependency := range row.DependsOn {
			target, ok := index[dependency]
			if !ok || target.Status != "completed" {
				dependencyBlocked++
				break
			}
		}
	}
	staleCompleted := 0
	for _, row := range rows {
		if row.Stale && row.Status == "completed" {
			staleCompleted++
		}
	}
	corrupt, conflicts, orphaned := 0, 0, 0
	for _, entry := range diagnostics {
		switch entry["health"] {
		case "corrupt":
			corrupt++
		case "conflict":
			conflicts++
		case "orphaned":
			orphaned++
		}
	}
	return map[string]any{
		"total":              len(rows),
		"by_status":          byStatus,
		"by_stage":           byStage,
		"by_priority":        byPriority,
		"archived":           archived,
		"dependency_blocked": dependencyBlocked,
		"needs_input":        byStatus["needs_input"],
		"blocked":            byStatus["blocked"],
		"running":            byStatus["running"],
		"completed":          byStatus["completed"],
		"planned":            byStatus["planned"],
		"cancelled":          byStatus["cancelled"],
		"stale_completed":    staleCompleted,
		"corrupt":            corrupt,
		"conflicts":          conflicts,
		"orphaned":           orphaned,
	}
}
