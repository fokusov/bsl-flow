package stagehost

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bsl-flow/cli/internal/repository"
)

// TestNative1CReadOnlyPowerShellParity runs the read-only native 1C bindings
// (source snapshot, runtime target key, JUnit gate) through both engines over
// one fixture and requires canonical byte-identical results. The read-only
// parts are the differential surface of the runtime adapter: the COM
// inventory and the 1cv8 dispatches stay Windows-runtime-only and are never
// re-executed for parity. It skips when pwsh or the scripts are absent.
func TestNative1CReadOnlyPowerShellParity(t *testing.T) {
	scripts := parityRepoScriptsDir(t)
	if scripts == "" {
		t.Skip("packaged skill scripts are unavailable")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh unavailable")
	}
	sourceRoot := t.TempDir()
	config := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Configuration uuid="A1B2C3D4-0000-0000-0000-000000000001">
		<Properties><Name>Ext</Name><Version>1.2.3</Version></Properties>
	</Configuration>
</MetaDataObject>`
	module := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Catalog uuid="A1B2C3D4-0000-0000-0000-000000000002">
		<Properties/>
	</Catalog>
</MetaDataObject>`
	if err := os.WriteFile(filepath.Join(sourceRoot, "Configuration.xml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "Catalogs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "Catalogs", "Goods.xml"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "1Cv8.1CD"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `<?xml version="1.0" encoding="UTF-8"?>
<testsuite tests="1" failures="0" errors="0" skipped="0" disabled="0">
	<testcase classname="Tests.Module" name="Test"/>
</testsuite>`
	reportPath := filepath.Join(t.TempDir(), "original.junit.xml")
	now := time.Now().UTC()
	if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(reportPath, now, now); err != nil {
		t.Fatal(err)
	}
	started := now.Add(-time.Minute).Format("2006-01-02T15:04:05.0000000Z")
	finished := now.Add(time.Minute).Format("2006-01-02T15:04:05.0000000Z")

	// Go side.
	goSnapshot, err := repository.StageHostNative1CSourceSnapshot(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	goKey, err := repository.StageHostNative1CTargetKey(target)
	if err != nil {
		t.Fatal(err)
	}
	goJUnit, err := repository.StageHostNative1CJUnit(reportPath, []string{"Tests.Module.Test"}, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	goCanonical, err := repository.StageHostCanonical(map[string]any{
		"source": goSnapshot, "key": goKey, "junit": goJUnit,
	})
	if err != nil {
		t.Fatal(err)
	}

	// PowerShell side.
	script := `
param([string]$ScriptsRoot,[string]$SourceRoot,[string]$Target,[string]$Report,[string]$Started,[string]$Finished,[string]$OutFile)
Set-StrictMode -Version Latest
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Runtime.ps1')){ . (Join-Path $ScriptsRoot $name) }
$source=Get-BFNativeSource $SourceRoot
$key=Get-BFRuntimeTargetKey $Target
$junit=Test-BFNativeJUnit $Report @('Tests.Module.Test') ([datetime]::Parse($Started)) ([datetime]::Parse($Finished))
[IO.File]::WriteAllText($OutFile,(Get-BFCanonicalJson ([ordered]@{source=$source;key=$key;junit=$junit})),[Text.UTF8Encoding]::new($false))
`
	scriptPath := filepath.Join(t.TempDir(), "native1c-parity.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(t.TempDir(), "ps.json")
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath, scripts, sourceRoot, target, reportPath, started, finished, outFile)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("pwsh parity: %v: %s", err, stderr.String())
	}
	psBytes, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	psObject, err := repository.DecodeObject(psBytes)
	if err != nil {
		t.Fatal(err)
	}
	psCanonical, err := repository.StageHostCanonical(psObject)
	if err != nil {
		t.Fatal(err)
	}
	if string(goCanonical) != string(psCanonical) {
		// Keep the diagnostic focused: the first differing field is enough.
		goLines := strings.Split(string(goCanonical), ",")
		psLines := strings.Split(string(psCanonical), ",")
		for index := 0; index < len(goLines) && index < len(psLines); index++ {
			if goLines[index] != psLines[index] {
				t.Fatalf("first differing canonical segment:\n  go: %s\n  ps: %s", goLines[index], psLines[index])
			}
		}
		t.Fatalf("canonical outputs differ in length (go %d, ps %d)\ngo: %s\nps: %s", len(goLines), len(psLines), fmt.Sprint(string(goCanonical)), fmt.Sprint(string(psCanonical)))
	}
}
