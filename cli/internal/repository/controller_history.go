package repository

import (
	"os"
	"path/filepath"
)

// Historical references are resolved through the copied manifest. Signed v1
// bytes keep their original paths even when the original worktree is gone.
func resolveHistoricalTaskFile(repository *Repository, taskID, relative string) (string, bool, error) {
	if err := validateRelativeNativePath(relative, false); err != nil {
		return "", false, err
	}
	directory, err := repository.taskDir(taskID)
	if err != nil {
		return "", false, err
	}
	manifestData, err := ReadFileBytes(filepath.Join(directory, "legacy-artifact-manifest.json"))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	manifest, err := decodeStrictObject(manifestData)
	if err != nil {
		return "", false, err
	}
	if err := validateAdoptionManifestShape(manifest); err != nil {
		return "", false, err
	}
	for _, raw := range anyItems(manifest["artifacts"]) {
		entry := asMap(raw)
		if entry["source_rel"] != relative {
			continue
		}
		path := filepath.Join(directory, filepath.FromSlash(asStringOr(entry["canonical_rel"])))
		if !withinRoot(directory, path) {
			return "", false, blocked("historical artifact escaped canonical task")
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return "", false, err
		}
		if int64(len(data)) != asIntOr(entry["size_bytes"]) || fileSHA256(data) != asStringOr(entry["raw_sha256"]) {
			return "", false, blocked("historical artifact bytes changed")
		}
		return path, true, nil
	}
	return "", false, nil
}

func nativePriorAttemptStart(repository *Repository, taskID, attemptID string) (map[string]any, error) {
	path, err := attemptDirectory(repository, taskID, attemptID)
	if err != nil {
		return nil, err
	}
	start, err := readStoredAttempt(path)
	if !os.IsNotExist(err) {
		return start, err
	}
	historical, found, err := resolveHistoricalTaskFile(repository, taskID, "attempts/"+attemptID+"/start.json")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, blocked("prior attempt start is missing")
	}
	data, err := ReadFileBytes(historical)
	if err != nil {
		return nil, err
	}
	return DecodeObject(data)
}
