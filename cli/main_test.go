package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testID = "01234567-89ab-4cde-8123-0123456789ab"

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
