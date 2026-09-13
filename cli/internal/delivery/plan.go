package delivery

import (
	"sort"
	"strings"
)

// ManifestFile is one entry of the accepted source manifest; deleted entries
// carry no bytes and only prune the published tree.
type ManifestFile struct {
	Path    string
	SHA256  string
	Deleted bool
}

// Manifest is the accepted source snapshot a publication must reproduce
// byte-for-byte against its baseline.
type Manifest struct {
	SchemaVersion int64
	Baseline      CommitID
	Files         []ManifestFile
}

// value projects the manifest into its canonical JSON shape.
func (m Manifest) value() any {
	files := make([]any, 0, len(m.Files))
	for _, file := range m.Files {
		entry := map[string]any{"path": file.Path, "deleted": file.Deleted}
		if !file.Deleted {
			entry["sha256"] = file.SHA256
		}
		files = append(files, entry)
	}
	return map[string]any{"schema_version": m.SchemaVersion, "baseline": string(m.Baseline), "files": files}
}

// AcceptedSource binds the accepted revision a plan or handoff may be built
// from: only the current implementation PASS of a completed task qualifies,
// and EvidenceHash must equal the SHA-256 of the canonical manifest bytes, so
// any change to the accepted source fails the freshness check.
type AcceptedSource struct {
	TaskID       string
	Status       string
	Verdict      string
	Mode         string
	EvidenceHash string
	Manifest     Manifest
}

// verifyAccepted rejects non-accepted sources and re-hashes the manifest
// bytes the caller supplied as the freshness anchor.
func verifyAccepted(accepted AcceptedSource) error {
	if !validUUID(accepted.TaskID) {
		return invalid("accepted source task identity is malformed")
	}
	if accepted.Status != "completed" || accepted.Verdict != "PASS" || accepted.Mode != "implement" {
		return blocked("delivery requires a completed accepted task")
	}
	if !validSHA256(accepted.EvidenceHash) {
		return invalid("delivery identity must be a SHA-256 value")
	}
	manifest := accepted.Manifest
	if manifest.SchemaVersion != 1 {
		return invalid("unsupported manifest schema version")
	}
	if !validCommitID(manifest.Baseline) {
		return invalid("publication baseline is not an object identity")
	}
	if len(manifest.Files) == 0 {
		return invalid("publication manifest is empty")
	}
	ordinal := make(map[string]bool, len(manifest.Files))
	folded := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if !validRelativePath(file.Path) {
			return invalid("unsafe manifest path: %s", file.Path)
		}
		key := strings.ToLower(file.Path)
		if ordinal[file.Path] || folded[key] {
			return invalid("duplicate or case-colliding manifest path: %s", file.Path)
		}
		ordinal[file.Path] = true
		folded[key] = true
		if file.Deleted {
			continue
		}
		if !validSHA256(file.SHA256) {
			return invalid("invalid source SHA-256 for %s", file.Path)
		}
	}
	digest, err := hashValue(manifest.value())
	if err != nil {
		return err
	}
	if digest != accepted.EvidenceHash {
		return blocked("current source does not match the accepted manifest")
	}
	return nil
}

// Plan is the verified publication plan derived from an accepted source and
// an exact target; its identity hash binds every field.
type Plan struct {
	Target       Target
	TaskID       string
	Baseline     CommitID
	EvidenceHash string
	Message      string
	Paths        []string
	PlanHash     string
}

// BuildPlan validates the target and the accepted source, requires every
// manifest path to stay inside the target's closed set of admitted paths, and
// derives the deterministic commit message and the sorted staging order.
func BuildPlan(target Target, accepted AcceptedSource) (Plan, error) {
	if err := target.Validate(); err != nil {
		return Plan{}, err
	}
	if err := verifyAccepted(accepted); err != nil {
		return Plan{}, err
	}
	paths := make([]string, 0, len(accepted.Manifest.Files))
	for _, file := range accepted.Manifest.Files {
		if !pathWithin(file.Path, target.AllowedPaths) {
			return Plan{}, blocked("manifest path is outside the admitted target paths: %s", file.Path)
		}
		if !file.Deleted {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	plan := Plan{
		Target:       target,
		TaskID:       accepted.TaskID,
		Baseline:     accepted.Manifest.Baseline,
		EvidenceHash: accepted.EvidenceHash,
		Message:      "bsl-flow: publish accepted source " + accepted.EvidenceHash + " for task " + accepted.TaskID,
		Paths:        paths,
	}
	digest, err := hashValue(plan.body())
	if err != nil {
		return Plan{}, err
	}
	plan.PlanHash = digest
	return plan, nil
}

// body projects the plan without its identity hash.
func (p Plan) body() map[string]any {
	return map[string]any{
		"schema_version": publicationSchema,
		"task_id":        p.TaskID,
		"remote":         p.Target.Remote,
		"ref":            p.Target.Ref,
		"authorized_by":  p.Target.AuthorizedBy,
		"allowed_paths":  p.Target.AllowedPaths,
		"baseline":       string(p.Baseline),
		"evidence_hash":  p.EvidenceHash,
		"message":        p.Message,
		"paths":          p.Paths,
	}
}

// Canonical returns the deterministic JSON bytes of the plan including its
// identity hash; plans built from equal accepted inputs are byte-identical.
func (p Plan) Canonical() ([]byte, error) {
	if err := p.verify(); err != nil {
		return nil, err
	}
	body := p.body()
	body["plan_hash"] = p.PlanHash
	return canonicalJSON(body)
}

// verify re-validates the plan and its identity hash.
func (p Plan) verify() error {
	if err := p.Target.Validate(); err != nil {
		return err
	}
	digest, err := hashValue(p.body())
	if err != nil {
		return err
	}
	if digest != p.PlanHash {
		return conflict("publication plan identity changed")
	}
	return nil
}
