package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed internal/resources/*
var resources embed.FS

const entrypoint = "global/skills/1c-task/scripts/Invoke-BSLFlowTask.ps1"

type invocation struct {
	command string
	action  string
	options map[string]string
}

var actions = map[string]string{"start": "Start", "status": "Status", "next": "Next", "run": "Run", "update": "Update", "resume": "Resume", "cancel": "Cancel", "record": "Record", "accept": "Accept", "deliver": "Deliver"}
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func parse(args []string) (invocation, error) {
	in := invocation{options: map[string]string{}}
	if len(args) == 1 && (args[0] == "help" || args[0] == "version") {
		in.command = args[0]
		return in, nil
	}
	if len(args) < 2 {
		return in, errors.New("expected help, version, task <action>, or runner run")
	}
	if args[0] == "task" && actions[args[1]] != "" {
		in.command, in.action = "task", actions[args[1]]
	} else if args[0] == "runner" && args[1] == "run" {
		in.command, in.action = "runner", "Serve"
	} else {
		return in, errors.New("expected help, version, task <start|status|next|run|update|resume|cancel|record|accept|deliver>, or runner run")
	}
	allowed := map[string]bool{"--project": true}
	if in.action == "Start" || in.action == "Update" || in.action == "Serve" {
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
	return in, nil
}

func engineArgs(in invocation, root string) ([]string, error) {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, filepath.FromSlash(entrypoint)), "-Action", in.action}
	for _, option := range [][2]string{{"--project", "-ProjectPath"}, {"--task", "-TaskId"}, {"--input", "-InputFile"}, {"--attempt", "-AttemptId"}, {"--codex", "-CodexPath"}} {
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

func run(args []string, out, errOut io.Writer) int {
	in, err := parse(args)
	if err != nil {
		return hostError(out, 2, in.options["--task"], err)
	}
	if in.command == "help" {
		fmt.Fprintln(out, "bsl-flow version\nbsl-flow help\nbsl-flow task <start|status|next|run|update|resume|cancel|record|accept|deliver> --project <path> [--task <uuid>] [--input <json>] [--attempt <uuid>] [--codex <exe>]\nbsl-flow runner run --project <path> --input <json> [--codex <exe>]\nTask start/update require --input; all task actions except start require --task; record requires --attempt; --codex is for task run/resume and runner run. UUIDs must be lowercase.\nRequires PowerShell 7, Git and the configured worker provider. Ctrl+C is not rollback; inspect the exact task and use task cancel/resume.")
		return 0
	}
	data, err := resources.ReadFile("internal/resources/bundle.zip")
	if err != nil {
		return hostError(out, 11, "", errors.New("embedded bundle missing; build with Build-BSLFlowCli.ps1"))
	}
	versionBytes, err := resources.ReadFile("internal/resources/version.txt")
	if err != nil {
		return hostError(out, 11, "", err)
	}
	bundle, err := readBundle(data, strings.TrimSpace(string(versionBytes)))
	if err != nil {
		return hostError(out, 11, "", err)
	}
	if in.command == "version" {
		_ = json.NewEncoder(out).Encode(map[string]interface{}{"schema_version": 1, "package": "bsl-flow", "version": bundle.version, "bundle_sha256": bundle.hash})
		return 0
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
