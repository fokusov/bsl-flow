package stagehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// FSProbe is the exported `bsl-flow __fs-probe` entry used by the sandboxed
// capability check.
func FSProbe(argument string, stdout io.Writer) error {
	return runFSProbe(argument, stdout)
}

// runFSProbe is the native replacement of the PowerShell filesystem probe the
// capability check used to run inside the managed sandbox. It receives one
// compact JSON object of named paths and probes them in the exact legacy
// order, classifying every result the same way UnauthorizedAccessException was
// classified before.
func runFSProbe(argument string, stdout io.Writer) error {
	var paths map[string]string
	if err := json.Unmarshal([]byte(argument), &paths); err != nil {
		return fmt.Errorf("probe paths must be a JSON object: %v", err)
	}
	result := map[string]string{}
	readProbe := func(name, path string) {
		_, err := os.ReadFile(path)
		switch {
		case err == nil:
			result[name+"_read"] = "allowed"
		case probeDenied(err):
			result[name+"_read"] = "denied"
		default:
			result[name+"_read"] = "error"
		}
	}
	writeProbe := func(name, path string) {
		if err := os.WriteFile(path, []byte("probe"), 0o644); err == nil {
			result[name+"_write"] = "allowed"
		} else if probeDenied(err) {
			result[name+"_write"] = "denied"
		} else {
			result[name+"_write"] = "error"
		}
	}
	for _, name := range []string{"canonical_current", "canonical_revision", "canonical_inputs"} {
		if paths[name+"_read"] != "" {
			readProbe(name, paths[name+"_read"])
		}
	}
	if paths["config_read"] != "" {
		readProbe("config", paths["config_read"])
	}
	for _, name := range []string{"canonical_current", "canonical_revision", "canonical_inputs", "config", "scratch", "source"} {
		if paths[name+"_write"] != "" {
			writeProbe(name, paths[name+"_write"])
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func probeDenied(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}
