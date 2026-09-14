package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

const testID = "01234567-89ab-4cde-8123-0123456789ab"

func TestPublicationRequiresSeparateInput(t *testing.T) {
	for _, action := range []string{"publish", "publish-resume"} {
		base := []string{"task", action, "--project", `C:\project`, "--task", testID}
		if _, err := parse(base); err == nil {
			t.Fatal("publication accepted without authorization input")
		}
		args := append(append([]string{}, base...), "--input", `C:\publication.json`)
		in, err := parse(args)
		if err != nil {
			t.Fatal(err)
		}
		want := "Publish"
		if action == "publish-resume" {
			want = "PublishResume"
		}
		argv, err := engineArgs(in, `C:\cache`)
		if err != nil || in.action != want || !reflect.DeepEqual(argv[7:], []string{"-Action", want, "-ProjectPath", `C:\project`, "-TaskId", testID, "-InputFile", `C:\publication.json`}) {
			t.Fatalf("unexpected publication arguments: %v %v", argv, err)
		}
		for _, extra := range [][]string{{"--runtime-auth", "stdin"}, {"--codex", "worker.exe"}, {"--attempt", testID}, {"--input", "other.json"}} {
			if _, err := parse(append(append([]string{}, args...), extra...)); err == nil {
				t.Fatalf("accepted unrelated publication option: %v", extra)
			}
		}
	}
}

func TestRuntimeAuthStaysOutOfArguments(t *testing.T) {
	for _, action := range []string{"run", "resume", "update"} {
		args := []string{"task", action, "--project", `C:\project`, "--task", testID, "--runtime-auth", "stdin"}
		if action == "update" {
			args = append(args, "--input", `C:\recovery.json`)
		}
		in, err := parse(args)
		if err != nil {
			t.Fatal(err)
		}
		argv, err := engineArgs(in, `C:\cache`)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(argv[len(argv)-2:], []string{"-RuntimeAuth", "stdin"}) {
			t.Fatalf("auth argv: %q", argv)
		}
	}
	for _, args := range [][]string{
		{"task", "run", "--project", ".", "--task", testID, "--runtime-auth", "password"},
		{"task", "status", "--project", ".", "--task", testID, "--runtime-auth", "stdin"},
		{"task", "start", "--project", ".", "--input", "request.json", "--runtime-auth", "stdin"},
	} {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted unsafe/inapplicable auth: %q", args)
		}
	}
}

func TestStrictCLI(t *testing.T) {
	valid := [][]string{{"help"}, {"version"}, {"task", "start", "--project", `C:\проект & $x`, "--input", `C:\запрос;evil.json`}, {"task", "record", "--project", ".", "--task", testID, "--attempt", testID}, {"task", "run", "--project", ".", "--task", testID, "--codex", `C:\codex.exe`}}
	for _, args := range valid {
		if _, err := parse(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	invalid := [][]string{nil, {"version", "extra"}, {"task", "force-pass"}, {"task", "start", "--project", "."}, {"task", "status", "--project", ".", "--task", testID, "--input", "x"}, {"task", "status", "--project", ".", "--task", testID, "--project", "other"}, {"task", "status", "--project", ".", "--task", "x; Write-Host evil"}, {"task", "start", "--project", ".", "--input", "x\ny"}, {"task", "start", "--project", ".", "--input", "--project"}, {"task", "start", "--project", ".", "--input=x"}}
	for _, args := range invalid {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestExplicitEngineSelectionIsValidatedAndNotForwardedToLegacy(t *testing.T) {
	for _, engine := range []string{"native", "legacy-powershell"} {
		args := []string{"task", "run", "--project", `C:\project`, "--task", testID, "--engine", engine}
		in, err := parse(args)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		argv, err := engineArgs(in, `C:\cache`)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range argv {
			if value == "--engine" || value == engine {
				t.Fatalf("engine selection leaked to legacy argv: %q", argv)
			}
		}
	}
	for _, args := range [][]string{
		{"task", "run", "--project", ".", "--task", testID, "--engine", "powershell"},
		{"task", "start", "--project", ".", "--input", "request.json", "--engine", "native"},
		{"runner", "run", "--project", ".", "--input", "runner.json", "--engine", "native"},
	} {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted unsupported engine selection: %q", args)
		}
	}
}

func TestProviderJSONRejectsDuplicateMembersAndTrailingDocuments(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":1,"Schema_Version":1}`,
		`{"nested":{"key":1,"key":2}}`,
		`{"ok":true}{"extra":true}`,
	} {
		if _, err := strictJSONDocument([]byte(input)); err == nil {
			t.Fatalf("accepted ambiguous provider JSON: %s", input)
		}
	}
	for _, input := range []string{`{"schema_version":1}`, "[1,{\"key\":true}]"} {
		if _, err := strictJSONDocument([]byte(input)); err != nil {
			t.Fatalf("rejected valid provider JSON %s: %v", input, err)
		}
	}
}

func TestProviderObjectRejectsNonCanonicalClosedKeys(t *testing.T) {
	for _, input := range []string{
		`{"SCHEMA_VERSION":1}`,
		`{"schema_version":1,"provider_contract":{"NAME":"bsl-flow.native-provider.windows-ps.v1"}}`,
		`{"schema_version":1,"artifacts":[{"Path":"artifact"}]}`,
	} {
		var observation repository.ExecuteObservation
		if err := decodeProviderObject([]byte(input), &observation); err == nil {
			t.Fatalf("accepted non-canonical provider key: %s", input)
		}
	}

	valid := `{"schema_version":1,"provider_contract":{"name":"bsl-flow.native-provider.windows-ps.v1","version":1,"host_sha256":"","provider_sha256":"","asset_manifest_sha256":""},"artifacts":[{"path":"artifact","sha256":"","size_bytes":0,"kind":"source"}]}`
	var observation repository.ExecuteObservation
	if err := decodeProviderObject([]byte(valid), &observation); err != nil {
		t.Fatalf("rejected canonical provider keys: %v", err)
	}
}

func TestProviderJSONBoundsWideAndDeepDocuments(t *testing.T) {
	var wide strings.Builder
	wide.Grow(nativeProviderJSONMaxObjectMembers * 8)
	wide.WriteByte('{')
	for index := 0; index <= nativeProviderJSONMaxObjectMembers; index++ {
		if index > 0 {
			wide.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&wide, "\"k%d\":0", index)
	}
	wide.WriteByte('}')
	if _, err := strictJSONDocument([]byte(wide.String())); err == nil || !strings.Contains(err.Error(), "members") {
		t.Fatalf("wide provider JSON was not bounded: %v", err)
	}

	deep := strings.Repeat("[", nativeProviderJSONMaxDepth+1) + "0" + strings.Repeat("]", nativeProviderJSONMaxDepth+1)
	if _, err := strictJSONDocument([]byte(deep)); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deep provider JSON was not bounded: %v", err)
	}
}

func TestArgumentsRemainData(t *testing.T) {
	in, err := parse([]string{"task", "start", "--project", `C:\проект & $x`, "--input", `C:\запрос;$(evil).json`})
	if err != nil {
		t.Fatal(err)
	}
	args, err := engineArgs(in, `C:\cache with spaces`)
	if err != nil {
		t.Fatal(err)
	}
	if args[len(args)-1] != `C:\запрос;$(evil).json` || args[5] != "-File" {
		t.Fatalf("unexpected argv: %q", args)
	}
	for _, arg := range args {
		if arg == "-Command" || arg == "-EncodedCommand" {
			t.Fatal("shell code launch")
		}
	}
}

func TestDeliveryAndRunnerContracts(t *testing.T) {
	cases := []struct {
		input []string
		want  []string
	}{
		{[]string{"task", "context", "--project", `C:\проект`, "--task", testID}, []string{"-Action", "Context", "-ProjectPath", `C:\проект`, "-TaskId", testID}},
		{[]string{"task", "deliver", "--project", `C:\проект`, "--task", testID}, []string{"-Action", "Deliver", "-ProjectPath", `C:\проект`, "-TaskId", testID}},
		{[]string{"runner", "run", "--project", `C:\проект`, "--input", `C:\runner.json`}, []string{"-Action", "Serve", "-ProjectPath", `C:\проект`, "-InputFile", `C:\runner.json`}},
		{[]string{"runner", "run", "--project", `C:\проект`, "--input", `C:\runner.json`, "--codex", `C:\codex.exe`}, []string{"-Action", "Serve", "-ProjectPath", `C:\проект`, "-InputFile", `C:\runner.json`, "-CodexPath", `C:\codex.exe`}},
	}
	for _, test := range cases {
		in, err := parse(test.input)
		if err != nil {
			t.Fatal(err)
		}
		args, err := engineArgs(in, `C:\cache`)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(args[7:], test.want) {
			t.Fatalf("%v: %q != %q", test.input, args[7:], test.want)
		}
	}
	invalid := [][]string{
		{"task", "context", "--project", "."},
		{"task", "context", "--project", ".", "--task", testID, "--input", "extra.json"},
		{"task", "deliver", "--project", "."},
		{"task", "deliver", "--project", ".", "--task", testID, "--input", "extra.json"},
		{"task", "deliver", "--project", ".", "--task", strings.ToUpper(testID)},
		{"runner", "run", "--project", "."},
		{"runner", "run", "--input", "runner.json"},
		{"runner", "run", "--project", ".", "--input", "runner.json", "--task", testID},
		{"runner", "run", "--project", ".", "--input", "runner.json", "--attempt", testID},
		{"runner", "start", "--project", ".", "--input", "runner.json"},
	}
	for _, args := range invalid {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func fixtureZip(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	files := map[string]string{"VERSION": "0.8.0-dev.1\n", entrypoint: "# fixture\n"}
	for key, value := range extra {
		files[key] = value
	}
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../evil", "global/../../evil", "C:/evil", "global/a:stream", `global\evil`, "global/NUL.txt", "global/end.", "global/end ", "global/a\x01", "global/skills/1c-task/scripts/invoke-bslflowtask.ps1", "global/skills"} {
		if _, err := readBundle(fixtureZip(t, map[string]string{name: "evil"}), "0.8.0-dev.1"); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := readBundle(fixtureZip(t, nil), "../bad"); err == nil {
		t.Fatal("accepted unsafe version")
	}
	if _, err := readBundle(fixtureZip(t, nil), "1.0.0"); err == nil {
		t.Fatal("accepted version mismatch")
	}
}

func TestArchiveRejectsSymlink(t *testing.T) {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	header := &zip.FileHeader{Name: "global/link"}
	header.SetMode(os.ModeSymlink | 0777)
	f, _ := w.CreateHeader(header)
	_, _ = f.Write([]byte("outside"))
	_ = w.Close()
	if _, err := readBundle(out.Bytes(), "1.0.0"); err == nil {
		t.Fatal("accepted link")
	}
}

func TestCachePublicationAndTamper(t *testing.T) {
	b, err := readBundle(fixtureZip(t, nil), "0.8.0-dev.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"content", "missing", "extra", "directory"} {
		t.Run(mutation, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "cache")
			root, err := ensureBundle(base, b)
			if err != nil {
				t.Fatal(err)
			}
			if again, err := ensureBundle(base, b); err != nil || again != root {
				t.Fatalf("cache reuse %s %v", again, err)
			}
			switch mutation {
			case "content":
				err = os.WriteFile(filepath.Join(root, "VERSION"), []byte("tampered"), 0600)
			case "missing":
				err = os.Remove(filepath.Join(root, "VERSION"))
			case "extra":
				err = os.WriteFile(filepath.Join(root, "unexpected.txt"), []byte("x"), 0600)
			case "directory":
				err = os.Mkdir(filepath.Join(root, "unexpected"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ensureBundle(base, b); err == nil {
				t.Fatal("tampering was accepted")
			}
		})
	}
}

func TestExtractionLockAndOrphan(t *testing.T) {
	base := t.TempDir()
	lock := filepath.Join(base, ".extract.lock")
	unlock, err := lockCache(lock)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockCache(lock); err == nil {
		second()
		t.Fatal("concurrent extraction lock accepted")
	}
	unlock()
	unlock, err = lockCache(lock)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	b, err := readBundle(fixtureZip(t, nil), "0.8.0-dev.1")
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(base, ".extract-orphan")
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBundle(base, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("removed another extraction attempt")
	}
}

func TestErrorEnvelope(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"task", "force-pass"}, &out, &out); code != 2 || !strings.Contains(out.String(), `"schema_version":1`) || !strings.Contains(out.String(), "BF_INVALID") {
		t.Fatalf("invalid envelope: %d %s", code, out.String())
	}
}

func TestSystemPowerShellUsesKnownProgramFilesPowerShell7(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the trusted machine-wide PowerShell 7 location is Windows-only")
	}
	programFiles, err := knownProgramFiles()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramFiles", filepath.Join(t.TempDir(), "attacker-program-files"))
	t.Setenv("SystemRoot", filepath.Join(t.TempDir(), "attacker-system-root"))
	shell, err := systemPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")
	if shell != want {
		t.Fatalf("resolved %q, want trusted PowerShell 7 path %q", shell, want)
	}
	if filepath.Base(shell) != "pwsh.exe" || strings.Contains(strings.ToLower(shell), "windowspowershell") {
		t.Fatalf("unexpected shell %q", shell)
	}
}

func TestSystemPowerShellFailsWithoutPowerShell7(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the pinned pwsh.exe contract is Windows-only; other platforms reject the legacy engine earlier")
	}
	missing := t.TempDir()
	shell, err := powerShell7At(missing)
	if err == nil || shell != "" {
		t.Fatalf("missing PowerShell 7 resolved to %q, %v", shell, err)
	}
	if !strings.Contains(err.Error(), "PowerShell 7 is required") || strings.Contains(strings.ToLower(err.Error()), "windowspowershell") {
		t.Fatalf("missing PowerShell 7 error is not explicit and fail-closed: %v", err)
	}
}

func TestLegacyEngineGate(t *testing.T) {
	if err := legacyEngineGate("windows"); err != nil {
		t.Fatalf("windows must allow the legacy engine: %v", err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		err := legacyEngineGate(goos)
		if err == nil || !strings.Contains(err.Error(), "unsupported on this platform") {
			t.Fatalf("goos %s must be rejected with an unsupported-platform error, got %v", goos, err)
		}
	}
}

func TestCapabilityCommandPrintsMachineModel(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"capability"}, &out, &out); code != 0 {
		t.Fatalf("capability exit %d: %s", code, out.String())
	}
	var caps struct {
		GOOS     string   `json:"goos"`
		GOArch   string   `json:"goarch"`
		Engines  []string `json:"engines"`
		Native1C struct {
			Status string `json:"status"`
		} `json:"native_1c"`
		Filesystem struct {
			AtomicRename bool `json:"atomic_rename"`
			Locks        bool `json:"locks"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal(out.Bytes(), &caps); err != nil {
		t.Fatalf("capability output is not JSON: %v: %s", err, out.String())
	}
	if caps.GOOS != "windows" || caps.GOArch != "amd64" {
		t.Fatalf("unexpected platform %s/%s", caps.GOOS, caps.GOArch)
	}
	if !caps.Filesystem.AtomicRename || !caps.Filesystem.Locks {
		t.Fatalf("expected probed filesystem capabilities on the trusted cache root: %+v", caps.Filesystem)
	}
	found := false
	for _, engine := range caps.Engines {
		if engine == "native" {
			found = true
		}
	}
	if !found || caps.Native1C.Status != "windows-only" {
		t.Fatalf("unexpected engine/native1c model: %+v", caps)
	}
}
