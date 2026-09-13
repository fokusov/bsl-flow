package repository

import (
	"fmt"
	"os"
	"path/filepath"
)

const legacyLabel = "(legacy v1 task)"

type legacySnapshot struct {
	row  Row
	hash string
	// origin is the worktree root whose checkout-local store holds the first
	// verified copy of this journal.
	origin string
}

func (r *Repository) legacyLocations() []string {
	return r.Worktrees()
}

// scanLegacy reads every checkout-local v1 journal across all worktrees without
// modifying their bytes. Divergent histories of one UUID are surfaced as
// diagnostics and never silently resolved to one copy.
func (r *Repository) scanLegacy() (map[string]legacySnapshot, []map[string]any) {
	snapshots := map[string]legacySnapshot{}
	diagnostics := []map[string]any{}
	invalidIDs := map[string]bool{}
	for _, root := range r.legacyLocations() {
		base := filepath.Join(root, ".bsl-flow", "tasks")
		if _, pathErr := SafePath(base); pathErr != nil {
			diagnostics = append(diagnostics, diagnostic("", "legacy", "corrupt", pathErr.Error()))
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !isUUID(entry.Name()) {
				continue
			}
			id := entry.Name()
			legacyDirectory := filepath.Join(base, id)
			chain, err := r.readChain(legacyDirectory)
			if err != nil {
				invalidIDs[id] = true
				diagnostics = append(diagnostics, diagnostic(id, "legacy", "corrupt", err.Error()))
				continue
			}
			if len(chain) == 0 {
				invalidIDs[id] = true
				diagnostics = append(diagnostics, diagnostic(id, "legacy", "orphaned", "task directory has no revisions"))
				continue
			}
			latest := chain[len(chain)-1]
			hash, err := Hash(latest)
			if err != nil {
				invalidIDs[id] = true
				diagnostics = append(diagnostics, diagnostic(id, "legacy", "corrupt", err.Error()))
				continue
			}
			if existing, ok := snapshots[id]; ok {
				if existing.hash != hash {
					invalidIDs[id] = true
					diagnostics = append(diagnostics, diagnostic(id, "legacy", "conflict", "divergent legacy history across worktrees"))
				}
				continue
			}
			row, rowErr := legacyRow(id, latest)
			if rowErr != nil {
				invalidIDs[id] = true
				diagnostics = append(diagnostics, diagnostic(id, "legacy", "corrupt", rowErr.Error()))
				continue
			}
			row.OriginWorktree = root
			snapshots[id] = legacySnapshot{row: row, hash: hash, origin: root}
		}
	}
	for id := range invalidIDs {
		delete(snapshots, id)
	}
	return snapshots, diagnostics
}

func (r *Repository) legacyCatalog(known map[string]string, canonicalIDs map[string]bool) ([]Row, []map[string]any) {
	snapshots, diagnostics := r.scanLegacy()
	rows := []Row{}
	for id, snapshot := range snapshots {
		if canonicalIDs[id] && known[id] == "" {
			// Canonical identity is reserved even when its journal is corrupt or
			// orphaned. Keep the diagnostic emitted by Catalog and do not surface a
			// same-ID legacy fallback as a healthy task.
			continue
		}
		if repositoryHash, ok := known[id]; ok {
			if repositoryHash == snapshot.hash {
				continue
			}
			diagnostics = append(diagnostics, diagnostic(id, "legacy", "conflict", "divergent history from repository store"))
			continue
		}
		rows = append(rows, snapshot.row)
	}
	return rows, diagnostics
}

// legacyChainResult reports every verified copy of a legacy journal. More than
// one distinct terminal hash is a conflict that must fail closed.
type legacyChainResult struct {
	chain  []map[string]any
	origin string
}

func (r *Repository) resolveLegacy(id string) (*legacyChainResult, []string, error) {
	if !isUUID(id) {
		return nil, nil, invalid("task id must be a lowercase UUID")
	}
	var copies []*legacyChainResult
	var failures []string
	for _, root := range r.legacyLocations() {
		legacyDirectory := filepath.Join(root, ".bsl-flow", "tasks", id)
		if _, pathErr := SafePath(legacyDirectory); pathErr != nil {
			failures = append(failures, pathErr.Error())
			continue
		}
		info, statErr := os.Lstat(legacyDirectory)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			failures = append(failures, fmt.Sprintf("cannot inspect legacy task journal: %v", statErr))
			continue
		}
		if !info.IsDir() {
			failures = append(failures, "legacy task journal path is not a directory")
			continue
		}
		chain, err := r.readChain(legacyDirectory)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if len(chain) == 0 {
			failures = append(failures, "legacy task journal has no revisions")
			continue
		}
		copies = append(copies, &legacyChainResult{chain: chain, origin: root})
	}
	if len(copies) == 0 {
		if len(failures) > 0 {
			return nil, failures, blocked("legacy task journal is unreadable: %s", failures[0])
		}
		return nil, nil, nil
	}
	hashes := map[string]bool{}
	for _, copy := range copies {
		hash, err := Hash(copy.chain[len(copy.chain)-1])
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		hashes[hash] = true
	}
	if len(hashes) > 1 {
		return nil, failures, conflict("divergent legacy histories across worktrees for task %s; inspect the checkout-local journals before reading", id)
	}
	if len(failures) > 0 {
		return nil, failures, blocked("legacy task journal has an unreadable copy: %s", failures[0])
	}
	return copies[0], failures, nil
}

func (r *Repository) checkLegacyConflict(id string, canonical map[string]any) error {
	resolved, _, err := r.resolveLegacy(id)
	if err != nil {
		return err
	}
	if resolved == nil {
		return nil
	}
	canonicalHash, err := Hash(canonical)
	if err != nil {
		return blocked("cannot hash canonical revision for comparison: %v", err)
	}
	legacyHash, err := Hash(resolved.chain[len(resolved.chain)-1])
	if err != nil {
		return blocked("cannot hash legacy revision for comparison: %v", err)
	}
	if canonicalHash != legacyHash {
		return conflict("canonical and legacy histories diverge across worktrees for task %s", id)
	}
	return nil
}

// ResolveRow returns the safe projection of a task from the repository store or
// any legacy checkout-local store.
func (r *Repository) ResolveRow(id string) (Row, error) {
	directory, err := r.taskDir(id)
	if err != nil {
		return Row{}, err
	}
	directoryExists := false
	if _, statErr := os.Lstat(directory); statErr == nil {
		directoryExists = true
	} else if !os.IsNotExist(statErr) {
		return Row{}, blocked("cannot inspect task journal: %v", statErr)
	}
	chain, err := r.readChain(directory)
	if err != nil {
		return Row{}, err
	}
	if len(chain) > 0 {
		// A canonical copy is authoritative for catalog merge, but a targeted
		// detail read must still fail closed when a same-ID legacy copy is
		// corrupt or divergent. Otherwise show/history would hide an ambiguity
		// that list already reports diagnostically.
		if legacyErr := r.checkLegacyConflict(id, chain[len(chain)-1]); legacyErr != nil {
			return Row{}, legacyErr
		}
		task, err := taskFromState(chain[len(chain)-1])
		if err != nil {
			return Row{}, err
		}
		row := rowFromTask(task, "repository")
		row.OriginWorktree = r.resolveOrigin(task.OriginWorktree)
		return row, nil
	}
	if directoryExists {
		return Row{}, blocked("task journal is orphaned: no revisions for %s", id)
	}
	resolved, _, err := r.resolveLegacy(id)
	if err != nil {
		return Row{}, err
	}
	if resolved != nil {
		row, rowErr := legacyRow(id, resolved.chain[len(resolved.chain)-1])
		if rowErr != nil {
			return Row{}, rowErr
		}
		row.OriginWorktree = resolved.origin
		return row, nil
	}
	return Row{}, invalid("task not found: %s", id)
}

// legacyRow builds the safe projection of a v1 controller state, including the
// closed controller projection fields (stage, next action, question, attempts,
// acceptance and evidence references).
func legacyRow(id string, state map[string]any) (Row, error) {
	status, _ := asString(state["status"])
	if status == "" {
		status = "registered"
	}
	updated, _ := asString(state["updated_at"])
	if updated == "" {
		updated, _ = asString(state["created_at"])
	}
	revision, _ := asInt(state["revision"])
	row := Row{
		TaskID:        id,
		Title:         legacyLabel,
		Lifecycle:     "controller",
		Status:        status,
		Priority:      "low",
		Labels:        []string{},
		DependsOn:     []string{},
		Revision:      revision,
		SchemaVersion: 1,
		UpdatedAt:     updated,
		Source:        "legacy",
		Health:        "ok",
	}
	projectPath, _ := asString(state["project_path"])
	if projectPath != "" {
		row.OriginWorktree = projectPath
	}
	if workerPath, ok := asString(state["worker_path"]); ok && workerPath != "" {
		row.Stale = pathMissing(workerPath)
		if row.Stale {
			row.Health = "stale"
		}
	}
	if legacyEvidenceIsStale(state["evidence"]) {
		row.Stale = true
		row.Health = "stale"
	}
	if stage, ok := asString(state["stage"]); ok {
		row.Stage = stage
	}
	row.NextAction = legacyNextAction(state)
	if question, ok := state["question"].(map[string]any); ok && question != nil {
		if text, ok := asString(question["text"]); ok && text != "" {
			row.Question = safeProjectionText(text)
		}
	}
	if blockers, ok := asStringSlice(state["blockers"]); ok && len(blockers) > 0 {
		row.Blockers = make([]string, 0, len(blockers))
		for _, blocker := range blockers {
			row.Blockers = append(row.Blockers, safeProjectionText(blocker))
		}
	}
	if attempts, ok := state["attempts"].([]any); ok {
		row.AttemptCount = len(attempts)
	}
	if acceptances, ok := state["acceptances"].([]any); ok {
		row.AcceptanceCount = len(acceptances)
	}
	if evidence, ok := state["evidence"].([]any); ok {
		refs := make([]string, 0, len(evidence))
		for _, item := range evidence {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			attemptID, _ := asString(entry["attempt_id"])
			if attemptID == "" {
				continue
			}
			refs = append(refs, safeProjectionText(attemptID))
		}
		row.EvidenceRefs = refs
	}
	return row, nil
}

// legacyNextAction mirrors the decision order of the controller Get-BFNext gate
// for projection purposes only; it authorizes nothing.
func legacyNextAction(state map[string]any) string {
	// Get-BFNext is controller-owned and cannot be reproduced safely from a
	// read-only legacy snapshot. Keep the field explicit, but never present a
	// heuristic status mapping as an authoritative next action.
	return "unknown"
}

func pathMissing(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err != nil || !info.IsDir()
}

func fileMissing(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err != nil || !info.Mode().IsRegular()
}

func legacyEvidenceIsStale(value any) bool {
	for _, raw := range anyItems(value) {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, rawHash := range anyItems(entry["raw_hashes"]) {
			hash, ok := rawHash.(map[string]any)
			if !ok {
				continue
			}
			path, ok := asString(hash["path"])
			if ok && filepath.IsAbs(path) && fileMissing(path) {
				return true
			}
		}
	}
	return false
}
