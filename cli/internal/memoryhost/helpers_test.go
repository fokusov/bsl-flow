package memoryhost

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// jsonNumberOf builds a json.Number the way the strict decoders do.
func jsonNumberOf(text string) json.Number { return json.Number(text) }

func mkdirAllForTest(path string) error { return os.MkdirAll(path, 0o755) }

func writeFileForTest(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func removeForTest(path string) error { return os.Remove(path) }
