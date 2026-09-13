package platform

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMachineCapabilitiesJSONShape(t *testing.T) {
	detected := Detect("darwin", "arm64", Capability{AtomicRenameReliable: true}, "/usr/bin/git")
	data, err := json.Marshal(detected)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	if shape["goos"] != "darwin" || shape["goarch"] != "arm64" {
		t.Fatalf("platform fields: %v", shape)
	}
	filesystem, ok := shape["filesystem"].(map[string]any)
	if !ok || filesystem["atomic_rename"] != true || filesystem["locks"] != false {
		t.Fatalf("filesystem shape: %v", shape["filesystem"])
	}
	if shape["git_installed"] != true {
		t.Fatalf("git_installed: %v", shape["git_installed"])
	}
	native1c, ok := shape["native_1c"].(map[string]any)
	if !ok || native1c["status"] != Native1CStatusUnsupported {
		t.Fatalf("native_1c shape: %v", shape["native_1c"])
	}
	if !reflect.DeepEqual(shape["engines"], []any{"native"}) {
		t.Fatalf("engines: %v", shape["engines"])
	}
	var round MachineCapabilities
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	if round.GOOS != detected.GOOS || round.GOArch != detected.GOArch {
		t.Fatalf("round trip platform: %+v", round)
	}
	if round.Filesystem != (FilesystemCapability{AtomicRename: true, Locks: false}) {
		t.Fatalf("round trip filesystem: %+v", round.Filesystem)
	}
	if round.Native1C.Status != Native1CStatusUnsupported || round.GitInstalled != true {
		t.Fatalf("round trip capabilities: %+v", round)
	}
	if !reflect.DeepEqual(round.Engines, []string{"native"}) {
		t.Fatalf("round trip engines: %v", round.Engines)
	}
}

func TestDetectWindowsKeepsLegacyEngine(t *testing.T) {
	detected := Detect("windows", "amd64", Capability{LockSupported: true}, "")
	if detected.Native1C.Status != Native1CStatusWindowsOnly {
		t.Fatalf("native_1c status: %q", detected.Native1C.Status)
	}
	if !reflect.DeepEqual(detected.Engines, []string{string(EngineNative), string(EngineLegacyPowerShell)}) {
		t.Fatalf("engines: %v", detected.Engines)
	}
	if detected.GitInstalled {
		t.Fatal("empty git path reported as installed")
	}
	if detected.Filesystem.AtomicRename || !detected.Filesystem.Locks {
		t.Fatalf("filesystem capabilities: %+v", detected.Filesystem)
	}
}

func TestNative1CBlocker(t *testing.T) {
	if blocker := Native1CBlocker("windows"); blocker != nil {
		t.Fatalf("windows blocked: %+v", blocker)
	}
	for _, goos := range []string{"darwin", "linux", "freebsd"} {
		blocker := Native1CBlocker(goos)
		if blocker == nil {
			t.Fatalf("%s not blocked", goos)
		}
		if blocker.Code != BlockerUnsupportedPlatform {
			t.Fatalf("%s blocker code: %q", goos, blocker.Code)
		}
		if blocker.Message == "" {
			t.Fatalf("%s blocker has no message", goos)
		}
	}
}
