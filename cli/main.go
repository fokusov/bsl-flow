package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"bsl-flow/cli/internal/platform"
	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/stagehost"
)

//go:embed internal/resources/*
var resources embed.FS

const entrypoint = "global/skills/1c-task/scripts/Invoke-BSLFlowTask.ps1"

type invocation struct {
	command string
	action  string
	options map[string]string
}

var actions = map[string]string{"start": "Start", "status": "Status", "next": "Next", "context": "Context", "run": "Run", "update": "Update", "resume": "Resume", "cancel": "Cancel", "record": "Record", "accept": "Accept", "deliver": "Deliver", "publish": "Publish", "publish-resume": "PublishResume"}
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func parse(args []string) (invocation, error) {
	in := invocation{options: map[string]string{}}
	if len(args) == 1 && (args[0] == "help" || args[0] == "version" || args[0] == "capability") {
		in.command = args[0]
		return in, nil
	}
	if len(args) < 2 {
		return in, errors.New("expected help, version, task <action>, or runner run")
	}
	if args[0] == "spec" {
		return parseSpecInvocation(args)
	}
	if args[0] == "task" && actions[args[1]] != "" {
		in.command, in.action = "task", actions[args[1]]
	} else if args[0] == "runner" && args[1] == "run" {
		in.command, in.action = "runner", "Serve"
	} else {
		return in, errors.New("expected help, version, task <start|status|next|context|run|update|resume|cancel|record|accept|deliver|publish|publish-resume>, or runner run")
	}
	allowed := map[string]bool{"--project": true}
	if in.action == "Start" || in.action == "Update" || in.action == "Serve" || in.action == "Publish" || in.action == "PublishResume" {
		allowed["--input"] = true
	}
	if in.command == "task" && in.action != "Start" {
		allowed["--task"] = true
	}
	if in.action == "Record" {
		allowed["--attempt"] = true
	}
	if in.action == "Run" || in.action == "Resume" || in.action == "Serve" {
		allowed["--codex"] = true
	}
	if in.action == "Run" || in.action == "Resume" || in.action == "Serve" || in.action == "Update" {
		allowed["--runtime-auth"] = true
	}
	if in.command == "task" {
		switch in.action {
		case "Status", "Next", "Context", "Run", "Resume", "Record", "Update", "Cancel", "Accept":
			allowed["--engine"] = true
		}
	}
	for i := 2; i < len(args); i += 2 {
		key := args[i]
		if !allowed[key] || in.options[key] != "" {
			return in, fmt.Errorf("unknown, repeated, or inapplicable option %q", key)
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") || strings.ContainsAny(args[i+1], "\x00\r\n") {
			return in, fmt.Errorf("missing or invalid value for %s", key)
		}
		in.options[key] = args[i+1]
	}
	for _, key := range []string{"--project", "--task", "--input", "--attempt"} {
		if allowed[key] && in.options[key] == "" {
			return in, fmt.Errorf("%s requires %s", args[1], key)
		}
	}
	for _, key := range []string{"--task", "--attempt"} {
		if value := in.options[key]; value != "" && !uuid.MatchString(value) {
			return in, fmt.Errorf("%s must be a lowercase UUID", key)
		}
	}
	if value := in.options["--runtime-auth"]; value != "" && value != "stdin" {
		return in, errors.New("--runtime-auth accepts only stdin; credentials must not appear in arguments")
	}
	if value := in.options["--engine"]; value != "" && value != "native" && value != "legacy-powershell" {
		return in, errors.New("--engine accepts only native or legacy-powershell")
	}
	return in, nil
}

func engineArgs(in invocation, root string) ([]string, error) {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, filepath.FromSlash(entrypoint)), "-Action", in.action}
	for _, option := range [][2]string{{"--project", "-ProjectPath"}, {"--task", "-TaskId"}, {"--input", "-InputFile"}, {"--attempt", "-AttemptId"}, {"--codex", "-CodexPath"}, {"--runtime-auth", "-RuntimeAuth"}} {
		value := in.options[option[0]]
		if value == "" {
			continue
		}
		if option[0] == "--project" || option[0] == "--input" || option[0] == "--codex" {
			var err error
			value, err = filepath.Abs(value)
			if err != nil {
				return nil, err
			}
		}
		args = append(args, option[1], value)
	}
	return args, nil
}

func hostError(out io.Writer, code int, task string, err error) int {
	prefix := "BF_BLOCKED: "
	if code == 2 {
		prefix = "BF_INVALID: "
	}
	var id interface{}
	if task != "" {
		id = task
	}
	_ = json.NewEncoder(out).Encode(map[string]interface{}{"schema_version": 1, "task_id": id, "revision": nil, "status": "blocked", "stage": nil, "next_action": "inspect_blocker", "blockers": []string{prefix + err.Error()}, "evidence_refs": []string{}})
	return code
}

// readEmbeddedBundle parses the exact bundle snapshot compiled into this
// executable. It performs no extraction or process lookup, so registry reads
// can remain independent of PowerShell and the cache.
func readEmbeddedBundle() (bundle, error) {
	data, err := resources.ReadFile("internal/resources/bundle.zip")
	if err != nil {
		return bundle{}, errors.New("embedded bundle missing; build with Build-BSLFlowCli.ps1")
	}
	versionBytes, err := resources.ReadFile("internal/resources/version.txt")
	if err != nil {
		return bundle{}, err
	}
	return readBundle(data, strings.TrimSpace(string(versionBytes)))
}

// newPackagedNativeResolver returns a lazy native host. The closure does not
// read the embedded bundle, touch the cache, resolve PowerShell, or hash the
// executable until the controller actually selects native execution. A
// failed resolution is retained, which prevents a later retry from silently
// selecting a different provider or bundle.
func newPackagedNativeResolver() *repository.ControllerHost {
	var once sync.Once
	var provider repository.Provider
	var identity repository.EngineIdentity
	var resolveErr error
	return &repository.ControllerHost{
		Resolve: func() (repository.Provider, repository.EngineIdentity, error) {
			once.Do(func() {
				b, err := readEmbeddedBundle()
				if err != nil {
					resolveErr = err
					return
				}
				cache, err := os.UserCacheDir()
				if err != nil {
					resolveErr = fmt.Errorf("native provider cache: %w", err)
					return
				}
				root, err := ensureBundle(filepath.Join(cache, "BSLFlow", "bundles"), b)
				if err != nil {
					resolveErr = fmt.Errorf("native provider bundle: %w", err)
					return
				}
				self, err := os.Executable()
				if err != nil {
					resolveErr = fmt.Errorf("native provider host executable: %w", err)
					return
				}
				self, err = filepath.Abs(self)
				if err != nil {
					resolveErr = fmt.Errorf("native provider host executable: %w", err)
					return
				}
				if err := checkPath(self); err != nil {
					resolveErr = fmt.Errorf("native provider host executable: %w", err)
					return
				}
				packaged, err := newNativeProvider(root, b, self)
				if err != nil {
					resolveErr = err
					return
				}
				native, err := newGoNativeProvider(root, b, self)
				if err != nil {
					resolveErr = err
					return
				}
				provider = &compositeNativeProvider{native: native, inner: packaged}
				identity = packaged.engineIdentity()
			})
			return provider, identity, resolveErr
		},
	}
}

func run(args []string, out, errOut io.Writer) int {
	// Hidden provider-mode subcommands are only ever invoked by this same
	// trusted binary: the native stage host and its sandbox filesystem probe.
	// They are deliberately absent from help output and rejected with options.
	if len(args) == 1 && args[0] == "__provider" {
		return runProviderMode(context.Background(), os.Stdin, out, errOut)
	}
	if len(args) == 2 && args[0] == "__fs-probe" {
		if err := stagehost.FSProbe(args[1], out); err != nil {
			fmt.Fprintln(errOut, err.Error())
			return 1
		}
		return 0
	}
	if handled, code := repository.DispatchWithHost(args, out, errOut, newPackagedNativeResolver()); handled {
		return code
	}
	in, err := parse(args)
	if err != nil {
		return hostError(out, 2, in.options["--task"], err)
	}
	if in.command == "spec" {
		return runSpecCommand(in, out)
	}
	if in.command == "help" {
		fmt.Fprintln(out, "bsl-flow version\nbsl-flow help\nbsl-flow capability\nbsl-flow task <start|status|next|context|run|update|resume|cancel|record|accept|deliver|publish|publish-resume> --project <path> [--task <uuid>] [--input <json>] [--attempt <uuid>] [--codex <exe>] [--engine <native|legacy-powershell>]\nbsl-flow runner run --project <path> --input <json> [--codex <exe>]\nTask start/update/publish/publish-resume require --input; all task actions except start require --task; record requires --attempt; --codex is for task run/resume and runner run. UUIDs must be lowercase.\nExecution routing defaults to native for canonical repository tasks and legacy-powershell for checkout-local v1 tasks; --engine makes a supported choice explicit.\nThe legacy-powershell engine exists only on Windows; other platforms reject it before task state access. bsl-flow capability prints the observed machine capability model.\nTask context is a read-only projection of the controller journal and Get-BFNext; it writes nothing and authorizes nothing.\nNative runtime auth uses --runtime-auth stdin on run/resume/update/runner; send one private JSON line with username and password. No credential files or secret arguments.\nPublication requires a separate exact acceptance/remote/ref authorization; publish-resume only reads the remote result.\nRequires PowerShell 7, Git and the configured worker provider. Ctrl+C is not rollback; inspect the exact task and use task cancel/resume.")
		fmt.Fprintln(out, "bsl-flow task <create|edit|activate|adopt|rebind|list|show|history|overview|archive|unarchive> --project <path> [--task <uuid>] [--input <json>] [--expected-revision <n>] [--source <path>] [--preview|--apply] [--json] [--human]")
		fmt.Fprintln(out, "Native repository task commands are clone-local operations; metadata reads default to JSON and --human prints tables. activate requires a trusted request and a compatible packaged provider, then publishes the ready revision. adopt previews or applies a checked legacy binding; rebind attaches the same UUID to a fresh trusted request. Execution of canonical tasks stays on the native route and never falls back to legacy PowerShell.")
		fmt.Fprintln(out, "bsl-flow spec lint --project <path> [--change <id>] [--json] | bsl-flow spec final --project <path> --change <id> [--json] — native PowerShell-free spec validation: lint prints the Test-1CSpec spec-lint artifact shape (exit 1 on any error finding) and final runs the deterministic Test-1CSpecFinal checks over one change directory (exit 1 when any check fails).")
		return 0
	}
	if in.command == "capability" {
		return printCapabilities(out)
	}
	bundle, err := readEmbeddedBundle()
	if err != nil {
		return hostError(out, 11, "", err)
	}
	if in.command == "version" {
		_ = json.NewEncoder(out).Encode(map[string]interface{}{"schema_version": 1, "package": "bsl-flow", "version": bundle.version, "bundle_sha256": bundle.hash})
		return 0
	}
	if err := legacyEngineGate(runtime.GOOS); err != nil {
		return hostError(out, 11, in.options["--task"], err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return hostError(out, 11, in.options["--task"], err)
	}
	root, err := ensureBundle(filepath.Join(cache, "BSLFlow", "bundles"), bundle)
	if err != nil {
		return hostError(out, 11, in.options["--task"], err)
	}
	self, err := os.Executable()
	if err == nil {
		err = checkPath(self)
	}
	if err != nil {
		return hostError(out, 11, in.options["--task"], err)
	}
	shell, err := systemPowerShell()
	if err != nil {
		return hostError(out, 11, in.options["--task"], err)
	}
	argv, err := engineArgs(in, root)
	if err != nil {
		return hostError(out, 2, in.options["--task"], err)
	}
	cmd := exec.Command(shell, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, errOut
	// Do not inherit a caller-supplied host identity, even with different casing.
	for _, item := range os.Environ() {
		if !strings.EqualFold(strings.SplitN(item, "=", 2)[0], "BSL_FLOW_HOST_PATH") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "BSL_FLOW_HOST_PATH="+self)
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return hostError(out, 11, in.options["--task"], fmt.Errorf("PowerShell launch failed: %w", err))
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// The legacy PowerShell engine exists only on Windows. On every other
// platform a legacy-bound request is rejected before any cache or task
// state access; native repository reads stay available.
func legacyEngineGate(goos string) error {
	if goos != "windows" {
		return errors.New("legacy powershell engine is unsupported on this platform")
	}
	return nil
}

// printCapabilities reports the observed machine capability model. Text or
// configuration claims cannot enable a capability absent here.
func printCapabilities(out io.Writer) int {
	var probe platform.Capability
	if root, err := platform.TrustedCacheRoot(); err == nil {
		if err = safeMkdir(root); err == nil {
			if probed, probeErr := platform.ProbeFilesystem(root); probeErr == nil {
				probe = probed
			}
		}
	}
	gitPath, _ := exec.LookPath("git")
	caps := platform.Detect(runtime.GOOS, runtime.GOARCH, probe, gitPath)
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(caps); err != nil {
		return hostError(out, 11, "", err)
	}
	return 0
}
