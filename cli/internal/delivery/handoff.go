package delivery

// Artifact is one delivered file bound to the accepted manifest.
type Artifact struct {
	Path  string
	Bytes []byte
}

// HandoffClock supplies the canonical UTC instant recorded on handoff
// receipts. Injecting a fixed function keeps receipt bytes deterministic; a
// nil clock or an empty value omits handed_off_at entirely so no hidden
// wall-clock ever enters the canonical bytes.
var HandoffClock func() string

// HandoffReceipt is the durable evidence that the exact accepted source was
// handed off locally; publication remains a separate explicit step.
type HandoffReceipt struct {
	SchemaVersion    int64
	SourceHash       string
	ArtifactHashes   map[string]string
	SourceFileCount  int
	DeletedFileCount int
	HandedOffAt      string
}

// Canonical returns the deterministic receipt bytes (sorted artifact keys, no
// HTML escaping) suitable for hashing or durable storage.
func (r HandoffReceipt) Canonical() ([]byte, error) {
	hashes := make(map[string]any, len(r.ArtifactHashes))
	for path, digest := range r.ArtifactHashes {
		hashes[path] = digest
	}
	value := map[string]any{
		"schema_version":     r.SchemaVersion,
		"source_hash":        r.SourceHash,
		"artifact_hashes":    hashes,
		"source_file_count":  int64(r.SourceFileCount),
		"deleted_file_count": int64(r.DeletedFileCount),
	}
	if r.HandedOffAt != "" {
		value["handed_off_at"] = r.HandedOffAt
	}
	return canonicalJSON(value)
}

// Handoff verifies every artifact byte against the accepted manifest and
// returns the sealed receipt. The artifact set must cover exactly the
// non-deleted manifest files: a missing, extra, duplicate or changed file
// blocks the handoff.
func Handoff(accepted AcceptedSource, artifacts []Artifact) (HandoffReceipt, error) {
	if err := verifyAccepted(accepted); err != nil {
		return HandoffReceipt{}, err
	}
	expected := make(map[string]string, len(accepted.Manifest.Files))
	deleted := 0
	for _, file := range accepted.Manifest.Files {
		if file.Deleted {
			deleted++
			continue
		}
		expected[file.Path] = file.SHA256
	}
	seen := make(map[string]bool, len(artifacts))
	hashes := make(map[string]string, len(expected))
	for _, artifact := range artifacts {
		if !validRelativePath(artifact.Path) {
			return HandoffReceipt{}, invalid("unsafe artifact path: %s", artifact.Path)
		}
		wanted, ok := expected[artifact.Path]
		if !ok {
			return HandoffReceipt{}, blocked("artifact is not part of the accepted manifest: %s", artifact.Path)
		}
		if seen[artifact.Path] {
			return HandoffReceipt{}, invalid("duplicate artifact path: %s", artifact.Path)
		}
		seen[artifact.Path] = true
		digest := sha256Hex(artifact.Bytes)
		if digest != wanted {
			return HandoffReceipt{}, blocked("delivery bytes do not match accepted manifest: %s", artifact.Path)
		}
		hashes[artifact.Path] = digest
	}
	if len(seen) != len(expected) {
		return HandoffReceipt{}, blocked("exact accepted handoff is unavailable; %d of %d accepted files are present", len(seen), len(expected))
	}
	var handedOffAt string
	if HandoffClock != nil {
		handedOffAt = HandoffClock()
	}
	return HandoffReceipt{
		SchemaVersion:    publicationSchema,
		SourceHash:       accepted.EvidenceHash,
		ArtifactHashes:   hashes,
		SourceFileCount:  len(expected),
		DeletedFileCount: deleted,
		HandedOffAt:      handedOffAt,
	}, nil
}
