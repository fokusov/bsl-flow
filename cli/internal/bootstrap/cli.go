// This file exposes the bootstrap domain (Inspect/Apply/Verify plus the
// comment-preserving merges) as the native `bsl-flow init` and
// `bsl-flow upgrade` command layer, porting the CLI surface of
// global/skills/1c-init-project/scripts/Initialize-BSLFlowProject.ps1 and
// Update-BSLFlowProject.ps1. It never invokes PowerShell or any external
// tool; all managed-file writes go through the domain Apply so the
// byte-parity guarantees of the package doc hold unchanged.
//
// PowerShell parameter → flag mapping (kebab-case; the PS scripts declare no
// parameter aliases):
//
//	Initialize-BSLFlowProject.ps1                Go CLI
//	-------------------------------------------  ---------------------------
//	-ProjectPath (Position=0, default cwd)       --project <path> (default: working directory)
//	-Explicit1CProject                           --explicit-1c-project
//	-SkipProjectUpgrade                          --skip-project-upgrade
//
//	Update-BSLFlowProject.ps1                    Go CLI
//	-------------------------------------------  ---------------------------
//	-ProjectPath (Mandatory, absolute)           --project <path>
//	-Apply                                       --apply
//	-PlanPath                                    --plan-path <path>
//
// -DevelopmentDatabasePath, -ConfigureTests, -ConfigureUi,
// -WorkstationProfilePath and -SourcePath only drive the packaged PowerShell
// test-environment script and are deliberately absent here; passing them is
// a usage error, never a silent PowerShell fallback.
//
// Exit codes: 0 on success; 1 (BF_BLOCKED) for every refusal or failure,
// the PowerShell scripts' nonzero throw exit; 2 (BF_INVALID) for argument
// problems, matching the root CLI's parse() convention.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Templates resolves packaged managed-file templates by project-relative
// path (AGENTS.md, bsl-flow.yaml, .gitignore, .bsl-flow/project.yaml). The
// wiring layer assigns it from the embedded bundle (the same pattern as
// memoryhost.PackageRoot); a nil source fails closed.
var Templates func(rel string) ([]byte, error)

// FrameworkVersion is the installed framework version used as the upgrade
// target for the sentinel version bump (Update-BSLFlowProject.ps1's
// $targetVersion). The wiring layer assigns it from the packaged version
// document; an empty value fails closed as soon as an upgrade needs it.
var FrameworkVersion string

// UsageError marks an argument problem: it renders as BF_INVALID and maps to
// exit code 2.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return "BF_INVALID: " + e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// BlockedError marks a refusal or failure: it renders as BF_BLOCKED and maps
// to exit code 1, the PowerShell throw exit.
type BlockedError struct{ Err error }

func (e *BlockedError) Error() string { return "BF_BLOCKED: " + e.Err.Error() }
func (e *BlockedError) Unwrap() error { return e.Err }

func usageErrorf(format string, args ...any) error {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

// blockedError wraps any error as BlockedError unless it already carries a
// BF_ classification, so unexpected domain errors stay fail-closed without
// double prefixes.
func blockedError(err error) error {
	if err == nil {
		return nil
	}
	var usage *UsageError
	var blocked *BlockedError
	if errors.As(err, &usage) || errors.As(err, &blocked) {
		return err
	}
	return &BlockedError{Err: err}
}

// Command runs the native bootstrap CLI (`bsl-flow init` / `bsl-flow
// upgrade`) with PS-equivalent flags, output and exit codes. Human output
// and plan JSON go to out; BF_* errors go to errOut. It reads and writes
// only managed project files through the domain layer.
func Command(args []string, out, errOut io.Writer) int {
	return runCommand(args, cliDeps{templates: Templates, frameworkVersion: FrameworkVersion}, out, errOut)
}

type cliDeps struct {
	templates        func(rel string) ([]byte, error)
	frameworkVersion string
}

func runCommand(args []string, deps cliDeps, out, errOut io.Writer) int {
	var err error
	switch {
	case len(args) == 0:
		err = usageErrorf("expected init or upgrade")
	case args[0] == "init":
		err = runInit(deps, args[1:], out)
	case args[0] == "upgrade":
		err = runUpgrade(deps, args[1:], out)
	default:
		err = usageErrorf("expected init or upgrade, got %q", args[0])
	}
	if err == nil {
		return 0
	}
	var usage *UsageError
	if errors.As(err, &usage) {
		fmt.Fprintln(errOut, err.Error())
		return 2
	}
	fmt.Fprintln(errOut, blockedError(err).Error())
	return 1
}

type flagSpec struct {
	name       string
	takesValue bool
}

// parseFlags applies the strict option discipline of the root CLI parse():
// unknown or repeated options are rejected, option values may not be empty,
// start with "--", or carry control characters, and switches take no value.
func parseFlags(args []string, specs []flagSpec) (map[string]string, error) {
	allowed := make(map[string]flagSpec, len(specs))
	for _, spec := range specs {
		allowed[spec.name] = spec
	}
	parsed := make(map[string]string, len(specs))
	for i := 0; i < len(args); i++ {
		key := args[i]
		spec, ok := allowed[key]
		if !ok || parsed[key] != "" {
			return nil, fmt.Errorf("unknown, repeated, or inapplicable option %q", key)
		}
		if !spec.takesValue {
			parsed[key] = "1"
			continue
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") || strings.ContainsAny(args[i+1], "\x00\r\n") {
			return nil, fmt.Errorf("missing or invalid value for %s", key)
		}
		parsed[key] = args[i+1]
		i++
	}
	return parsed, nil
}

type initOptions struct {
	project     string
	explicit1C  bool
	skipUpgrade bool
}

// runInit ports Initialize-BSLFlowProject.ps1 for the managed-file domain:
// read-only preflight refusals in the script's order, then Inspect → Apply
// → upgrade step → Verify, then the PS summary output. Git and OpenSpec
// scaffolding stay with the external tooling (package doc).
func runInit(deps cliDeps, args []string, out io.Writer) error {
	flags, err := parseFlags(args, []flagSpec{
		{name: "--project", takesValue: true},
		{name: "--explicit-1c-project"},
		{name: "--skip-project-upgrade"},
	})
	if err != nil {
		return usageErrorf("%s", err)
	}
	options := initOptions{
		project:     flags["--project"],
		explicit1C:  flags["--explicit-1c-project"] != "",
		skipUpgrade: flags["--skip-project-upgrade"] != "",
	}
	if options.project == "" {
		working, err := os.Getwd()
		if err != nil {
			return blockedError(fmt.Errorf("cannot determine the default project directory: %w", err))
		}
		options.project = working
	}
	if deps.templates == nil {
		return blockedError(errors.New("packaged bootstrap templates are unavailable"))
	}

	// Preflight, Initialize-BSLFlowProject.ps1:133-222. Every check is
	// read-only; any refusal aborts before a single byte is written.
	info, statErr := os.Stat(options.project)
	if statErr != nil || !info.IsDir() {
		return blockedError(fmt.Errorf("Project directory does not exist: %s", options.project))
	}
	root, err := filepath.Abs(options.project)
	if err != nil {
		return blockedError(err)
	}
	if err := validateProjectRoot(root); err != nil {
		return blockedError(err)
	}
	if !confirmed1CProject(root, options.explicit1C) {
		return blockedError(errors.New("The directory is not confirmed as a 1C project root. Add --explicit-1c-project only after the user explicitly identifies this exact directory as the intended 1C project."))
	}
	repoRoot, hasRepo := enclosingGitRepo(root)
	if hasRepo && !strings.EqualFold(repoRoot, root) {
		return blockedError(fmt.Errorf("The selected directory is inside another Git repository: %s. Confirm the real project root before bootstrap.", repoRoot))
	}
	if err := refuseOpenSpecSchemaConflict(root); err != nil {
		return blockedError(err)
	}
	if err := refuseForeignSentinel(root); err != nil {
		return blockedError(err)
	}
	for _, rel := range []string{"AGENTS.md", "bsl-flow.yaml", ".gitignore", ".bsl-flow/project.yaml", "openspec/config.yaml"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if info, err := os.Stat(full); err == nil && info.IsDir() {
			return blockedError(fmt.Errorf("A directory exists where a managed file is required: %s", full))
		}
	}
	for _, rel := range []string{"AGENTS.md", "bsl-flow.yaml", ".gitignore", ".bsl-flow/project.yaml"} {
		if _, err := deps.templates(rel); err != nil {
			return blockedError(fmt.Errorf("Bootstrap template is missing from the installed skill: %s", rel))
		}
	}

	// Upgrade preflight (Initialize-BSLFlowProject.ps1:219-222): a read-only
	// plan whose refusals abort init before any mutation; Write-Host parity
	// prints the plan document.
	if !options.skipUpgrade && fileExists(filepath.Join(root, "bsl-flow.yaml")) && fileExists(filepath.Join(root, ".bsl-flow", "project.yaml")) {
		if _, err := upgradeProject(deps, root, false, "", out); err != nil {
			return blockedError(err)
		}
	}

	// Managed files: Inspect → Apply with the sentinel template's
	// __INITIALIZED_AT__ placeholder stamped like the PS script does at
	// creation time; every other template byte passes through untouched.
	templates := initTemplates(deps.templates, time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"))
	plan, err := Inspect(root, templates)
	if err != nil {
		return blockedError(err)
	}
	applied, err := Apply(initPlanForApply(plan, options.skipUpgrade), templates)
	if err != nil {
		return blockedError(err)
	}

	if !options.skipUpgrade {
		if _, err := upgradeProject(deps, root, true, "", out); err != nil {
			return blockedError(err)
		}
	}

	// Final validation mirrors Initialize-BSLFlowProject.ps1:356-365 for the
	// file set this command owns: required managed files must exist. The
	// domain satisfaction check (Verify) is additionally enforced when the
	// upgrade step ran, since PS's Update -Apply must have left every
	// managed file satisfying the template; with -SkipProjectUpgrade the PS
	// script equally leaves existing text files unsatisfied by design.
	for _, managed := range managedFiles {
		if !fileExists(filepath.Join(root, filepath.FromSlash(managed.RelPath))) {
			return blockedError(fmt.Errorf("Final bootstrap validation failed; required file is missing: %s", managed.RelPath))
		}
	}
	if !options.skipUpgrade {
		if ok, problems := Verify(root, templates); !ok {
			return blockedError(fmt.Errorf("Final bootstrap validation failed: %s", strings.Join(problems, "; ")))
		}
	}

	created, preserved := initReport(applied, root, !options.skipUpgrade, hasRepo)
	fmt.Fprintf(out, "BSL Flow project bootstrap complete: %s\n", root)
	fmt.Fprintf(out, "Created: %d\n", len(created))
	for _, item := range created {
		fmt.Fprintf(out, "  + %s\n", item)
	}
	fmt.Fprintf(out, "Preserved: %d\n", len(preserved))
	for _, item := range preserved {
		fmt.Fprintf(out, "  = %s\n", item)
	}
	// Divergence from Initialize-BSLFlowProject.ps1: the native command does
	// not run git init or `openspec init --tools none` (external tools stay
	// outside the ported bootstrap domain), so brand-new projects need those
	// once. The note keeps that gap visible instead of implying the full PS
	// scaffolding happened.
	if !hasRepo || !dirExists(filepath.Join(root, "openspec")) {
		fmt.Fprintln(out, "Note: native bootstrap manages the packaged project files only; run git init and openspec init separately for new scaffolding.")
	}
	return nil
}

// initTemplates stamps the sentinel template's __INITIALIZED_AT__
// placeholder exactly where Initialize-BSLFlowProject.ps1:317 stamps it
// (creation time only - an existing sentinel is skipped before its template
// is ever read). The layout mirrors .NET's UtcNow.ToString('o'): always
// seven fractional digits and a Z suffix.
func initTemplates(templates func(rel string) ([]byte, error), initializedAt string) func(rel string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		data, err := templates(rel)
		if err != nil {
			return nil, err
		}
		if rel == ".bsl-flow/project.yaml" {
			return []byte(strings.ReplaceAll(string(data), "__INITIALIZED_AT__", initializedAt)), nil
		}
		return data, nil
	}
}

// initPlanForApply narrows a plan to the file actions
// Initialize-BSLFlowProject.ps1 performs before its upgrade step: with
// -SkipProjectUpgrade an existing AGENTS.md or bsl-flow.yaml is only
// preserved (copy-if-missing), while the marked .gitignore block is still
// appended or refreshed (lines 285-309).
func initPlanForApply(plan Plan, skipUpgrade bool) Plan {
	if !skipUpgrade {
		return plan
	}
	files := make([]FilePlan, 0, len(plan.Files))
	for _, file := range plan.Files {
		if file.Action == ActionMerge && (file.RelPath == "AGENTS.md" || file.RelPath == "bsl-flow.yaml") {
			file.Action = ActionSkip
			file.Reason = "file already exists and is preserved"
		}
		files = append(files, file)
	}
	return Plan{ProjectRoot: plan.ProjectRoot, Files: files}
}

// initReport builds the created/preserved summary lists in the PS shape;
// .gitignore merges use the script's managed-block labels
// (Initialize-BSLFlowProject.ps1:299,305), and the two upgrade notes come
// from lines 328-329.
func initReport(applied []AppliedFile, root string, ranUpgrade, hasRepo bool) (created, preserved []string) {
	for _, result := range applied {
		switch result.Action {
		case ActionCreate:
			created = append(created, result.RelPath)
		case ActionMerge:
			if result.RelPath == ".gitignore" {
				if strings.Contains(result.Reason, "absent") {
					created = append(created, ".gitignore managed block")
				} else {
					created = append(created, ".gitignore managed block updated")
				}
			} else {
				created = append(created, result.RelPath)
			}
		default:
			preserved = append(preserved, result.RelPath)
		}
	}
	if hasRepo {
		preserved = append(preserved, ".git")
	}
	if dirExists(filepath.Join(root, "openspec")) {
		preserved = append(preserved, "openspec")
		if schemaIsBSLFlow(filepath.Join(root, "openspec", "config.yaml")) {
			preserved = append(preserved, "openspec/config.yaml")
		}
	}
	if ranUpgrade {
		preserved = append(preserved,
			"bsl-flow.yaml user values/comments; missing framework keys merged",
			"AGENTS.md user instructions; BSL Flow managed block merged")
	}
	return created, preserved
}

// runUpgrade ports Update-BSLFlowProject.ps1 as a CLI subcommand: plan by
// default, apply only with --apply.
func runUpgrade(deps cliDeps, args []string, out io.Writer) error {
	flags, err := parseFlags(args, []flagSpec{
		{name: "--project", takesValue: true},
		{name: "--apply"},
		{name: "--plan-path", takesValue: true},
	})
	if err != nil {
		return usageErrorf("%s", err)
	}
	project := flags["--project"]
	if project == "" {
		return usageErrorf("upgrade requires --project")
	}
	// Update-BSLFlowProject.ps1:13-16 requires absolute paths. Divergence:
	// the PS regex only recognizes drive and UNC forms, which would reject
	// every ordinary Linux/macOS root; the port accepts any native absolute
	// path (filepath.IsAbs) with the same PS refusal message.
	if !filepath.IsAbs(project) {
		return blockedError(errors.New("ProjectPath must be an absolute filesystem path."))
	}
	if planPath := flags["--plan-path"]; planPath != "" && !filepath.IsAbs(planPath) {
		return blockedError(errors.New("PlanPath must be an absolute filesystem path."))
	}
	if deps.templates == nil {
		return blockedError(errors.New("packaged bootstrap templates are unavailable"))
	}
	if _, err := upgradeProject(deps, filepath.Clean(project), flags["--apply"] != "", flags["--plan-path"], out); err != nil {
		return blockedError(err)
	}
	return nil
}

// upgradePlan is the deterministic plan document of
// Update-BSLFlowProject.ps1 (ConvertTo-Json shape, property order
// preserved; the applied flag is appended after a successful apply exactly
// like Add-Member).
type upgradePlan struct {
	SchemaVersion             int                 `json:"schema_version"`
	Project                   string              `json:"project"`
	InstalledFrameworkVersion string              `json:"installed_framework_version"`
	ProjectFrameworkVersion   string              `json:"project_framework_version"`
	Status                    string              `json:"status"`
	Actions                   []upgradePlanAction `json:"actions"`
	ApplyRequested            bool                `json:"apply_requested"`
	Applied                   bool                `json:"applied,omitempty"`
}

type upgradePlanAction struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

func marshalUpgradePlan(plan *upgradePlan) (string, error) {
	if plan.Actions == nil {
		plan.Actions = []upgradePlanAction{}
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// writePlanFile stores the pre-apply plan document
// (Update-BSLFlowProject.ps1:232-236): UTF-8 without BOM plus one trailing
// platform newline, parent directories created on demand.
func writePlanFile(path string, document string) error {
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create plan directory: %w", err)
	}
	return os.WriteFile(path, []byte(document+newline), 0o644)
}

// upgradeProject ports the body of Update-BSLFlowProject.ps1: preflight
// refusals, deterministic plan computation and print, optional plan-file
// write, then the managed writes when apply was requested and actions
// exist. Managed-file content always goes through the domain Apply; the
// sentinel version bump (Set-SentinelVersion) is the one write the CLI
// layer owns, byte-compatible with the PS regex splice.
func upgradeProject(deps cliDeps, root string, apply bool, planPath string, out io.Writer) (*upgradePlan, error) {
	configPath := filepath.Join(root, "bsl-flow.yaml")
	sentinelPath := filepath.Join(root, ".bsl-flow", "project.yaml")
	if !fileExists(configPath) {
		return nil, fmt.Errorf("Missing project configuration: %s", configPath)
	}
	if !fileExists(sentinelPath) {
		return nil, fmt.Errorf("Missing project sentinel: %s", sentinelPath)
	}
	if deps.frameworkVersion == "" {
		return nil, errors.New("installed framework version is unknown")
	}
	sentinelBytes, err := os.ReadFile(sentinelPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read project sentinel: %w", err)
	}
	sentinelText := string(sentinelBytes)
	framework := sentinelFrameworkLine.FindStringSubmatch(sentinelText)
	if framework == nil || strings.Trim(framework[1], "\"'") != "bsl-flow" {
		return nil, errors.New("Project sentinel is not owned by bsl-flow.")
	}
	currentVersion := "unknown"
	if match := sentinelVersionLine.FindStringSubmatch(sentinelText); match != nil {
		currentVersion = strings.Trim(match[1], "\"'")
	}
	if currentVersion != "unknown" {
		comparison, err := compareBFVersion(currentVersion, deps.frameworkVersion)
		if err != nil {
			return nil, err
		}
		if comparison > 0 {
			return nil, fmt.Errorf("Project version %s is newer than installed framework %s.", currentVersion, deps.frameworkVersion)
		}
	}
	templates := make(map[string]string, 3)
	for _, rel := range []string{"bsl-flow.yaml", ".gitignore", "AGENTS.md"} {
		data, err := deps.templates(rel)
		if err != nil {
			return nil, fmt.Errorf("Missing packaged template: %s", rel)
		}
		templates[rel] = strings.TrimPrefix(string(data), utf8BOM)
	}

	plan := &upgradePlan{
		SchemaVersion:             1,
		Project:                   root,
		InstalledFrameworkVersion: deps.frameworkVersion,
		ProjectFrameworkVersion:   currentVersion,
		Actions:                   []upgradePlanAction{},
		ApplyRequested:            apply,
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read project configuration: %w", err)
	}
	added, err := missingConfigPaths(string(configBytes), templates["bsl-flow.yaml"])
	if err != nil {
		return nil, err
	}
	for _, path := range added {
		plan.Actions = append(plan.Actions, upgradePlanAction{Action: "add_managed_path", Path: path})
	}
	gitIgnorePath := filepath.Join(root, ".gitignore")
	if info, statErr := os.Stat(gitIgnorePath); statErr == nil && info.IsDir() {
		return nil, fmt.Errorf("A directory exists where .gitignore is required: %s", gitIgnorePath)
	}
	if updated, err := plannedManagedFile(kindGitIgnore, ".gitignore", gitIgnorePath, templates[".gitignore"]); err != nil {
		return nil, err
	} else if updated {
		plan.Actions = append(plan.Actions, upgradePlanAction{Action: "update_managed_gitignore", Path: ".gitignore"})
	}
	agentsPath := filepath.Join(root, "AGENTS.md")
	if info, statErr := os.Stat(agentsPath); statErr == nil && info.IsDir() {
		return nil, fmt.Errorf("A directory exists where AGENTS.md is required: %s", agentsPath)
	}
	agentsExists := fileExists(agentsPath)
	if updated, err := plannedManagedFile(kindAgents, "AGENTS.md", agentsPath, templates["AGENTS.md"]); err != nil {
		return nil, err
	} else if updated {
		action := "update_managed_agents"
		if !agentsExists {
			action = "create_agents"
		}
		plan.Actions = append(plan.Actions, upgradePlanAction{Action: action, Path: "AGENTS.md"})
	}
	newSentinel, err := updatedSentinelVersion(sentinelText, deps.frameworkVersion)
	if err != nil {
		return nil, err
	}
	if newSentinel != sentinelText {
		plan.Actions = append(plan.Actions, upgradePlanAction{
			Action: "update_framework_version",
			Path:   ".bsl-flow/project.yaml",
			From:   currentVersion,
			To:     deps.frameworkVersion,
		})
	}
	plan.Status = "up_to_date"
	if len(plan.Actions) > 0 {
		plan.Status = "changes_planned"
	}

	document, err := marshalUpgradePlan(plan)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, document)
	if planPath != "" {
		if err := writePlanFile(planPath, document); err != nil {
			return nil, err
		}
	}
	if !apply || len(plan.Actions) == 0 {
		return plan, nil
	}

	if err := applyUpgrade(deps, root); err != nil {
		return nil, err
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(sentinelPath); statErr == nil && info.Mode().Perm() != 0 {
		mode = info.Mode().Perm()
	}
	if err := atomicWrite(sentinelPath, []byte(newSentinel), mode); err != nil {
		return nil, err
	}
	plan.Applied = true
	plan.Status = "applied"
	postDocument, err := marshalUpgradePlan(plan)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, postDocument)
	return plan, nil
}

// missingConfigPaths lists the managed key paths the upgrade would insert.
// It reuses the domain merge decision itself, so the plan can never drift
// from what Apply will write: the domain detail string carries exactly
// Add-MissingTemplateNodes' added-path list.
func missingConfigPaths(currentText, templateText string) ([]string, error) {
	merged, detail, err := mergeManagedContent(kindConfig, "bsl-flow.yaml", []byte(currentText), templateSource(templateText))
	if err != nil {
		return nil, err
	}
	if merged == nil {
		return nil, nil
	}
	const prefix = "missing managed keys: "
	if !strings.HasPrefix(detail, prefix) {
		return nil, fmt.Errorf("unexpected bsl-flow.yaml merge detail: %s", detail)
	}
	if rest := strings.TrimPrefix(detail, prefix); rest != "" {
		return strings.Split(rest, ", "), nil
	}
	return nil, nil
}

func templateSource(template string) func(rel string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(template), nil }
}

// plannedManagedFile reports whether the managed file needs a change,
// reusing the domain merge decision; an absent file counts as a change
// because Update-BSLFlowProject.ps1:151,167 creates it from the template.
func plannedManagedFile(kind, rel, full, template string) (bool, error) {
	if !fileExists(full) {
		return true, nil
	}
	current, err := os.ReadFile(full)
	if err != nil {
		return false, fmt.Errorf("cannot read managed file %s: %w", full, err)
	}
	merged, _, err := mergeManagedContent(kind, rel, current, templateSource(template))
	if err != nil {
		return false, err
	}
	return merged != nil, nil
}

// applyUpgrade writes the three text managed files through the domain
// Apply, confined to the Update-BSLFlowProject.ps1 file set (the evidence
// placeholders and the sentinel belong to init / the version bump).
// Divergence: the PS script restores all originals when a later write
// fails; the domain provides per-file atomic writes instead, which this
// port keeps rather than bypassing Apply.
func applyUpgrade(deps cliDeps, root string) error {
	plan, err := Inspect(root, deps.templates)
	if err != nil {
		return err
	}
	files := make([]FilePlan, 0, 3)
	for _, file := range plan.Files {
		switch file.RelPath {
		case "AGENTS.md", "bsl-flow.yaml", ".gitignore":
			files = append(files, file)
		}
	}
	if _, err := Apply(Plan{ProjectRoot: root, Files: files}, deps.templates); err != nil {
		return err
	}
	return nil
}

var (
	openSpecSchemaLine    = regexp.MustCompile(`(?m)^\s*schema:\s*([^\s#]+)`)
	sentinelFrameworkLine = regexp.MustCompile(`(?m)^\s*framework:\s*([^\s#]+)`)
	sentinelVersionLine   = regexp.MustCompile(`(?m)^\s*framework_version:\s*([^\s#]+)`)
	bfVersionPattern      = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?$`)
)

// configuredSchemas ports Get-ConfiguredSchemas
// (Initialize-BSLFlowProject.ps1:52-58).
func configuredSchemas(text string) []string {
	var schemas []string
	for _, match := range openSpecSchemaLine.FindAllStringSubmatch(text, -1) {
		schemas = append(schemas, strings.Trim(match[1], "\"'"))
	}
	return schemas
}

func schemaIsBSLFlow(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	schemas := configuredSchemas(string(data))
	return len(schemas) == 1 && schemas[0] == "bsl-flow"
}

// refuseOpenSpecSchemaConflict ports the preflight refusal of
// Initialize-BSLFlowProject.ps1:180-193: an existing openspec/config.yaml
// must not carry duplicate or foreign schema keys.
func refuseOpenSpecSchemaConflict(root string) error {
	path := filepath.Join(root, "openspec", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	schemas := configuredSchemas(string(data))
	if len(schemas) > 1 {
		return fmt.Errorf("OpenSpec config contains duplicate schema keys. Resolve the ambiguous YAML before bootstrap: %s", path)
	}
	if len(schemas) == 1 && schemas[0] != "bsl-flow" {
		return fmt.Errorf("OpenSpec is already configured with schema '%s'. Resolve this workflow conflict before bootstrap.", schemas[0])
	}
	return nil
}

// refuseForeignSentinel ports Initialize-BSLFlowProject.ps1:195-202: an
// existing sentinel owned by another framework blocks the bootstrap.
func refuseForeignSentinel(root string) error {
	path := filepath.Join(root, ".bsl-flow", "project.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if match := sentinelFrameworkLine.FindStringSubmatch(string(data)); match != nil && strings.Trim(match[1], "\"'") != "bsl-flow" {
		return fmt.Errorf("The existing project sentinel belongs to another framework: %s", path)
	}
	return nil
}

// confirmed1CProject ports Test-Confirmed1CProject
// (Initialize-BSLFlowProject.ps1:65-108) with filesystem checks only: the
// explicit flag, the project sentinel, a strong 1C file or directory name
// in the root, at least two recognized source directories, or a strong 1C
// file within four directory levels below a source directory.
func confirmed1CProject(root string, explicit bool) bool {
	if explicit {
		return true
	}
	if fileExists(filepath.Join(root, ".bsl-flow", "project.yaml")) {
		return true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	var sourceDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			if isStrong1CFileName(entry.Name()) || isStrong1CExtension(filepath.Ext(entry.Name())) {
				return true
			}
			continue
		}
		if isStrong1CDirectoryName(entry.Name()) {
			return true
		}
		if lower := strings.ToLower(entry.Name()); lower == "src" || lower == "cf" || lower == "cfe" || lower == "edt" {
			sourceDirs = append(sourceDirs, filepath.Join(root, entry.Name()))
		}
	}
	if len(sourceDirs) >= 2 {
		return true
	}
	for _, dir := range sourceDirs {
		if containsStrong1CIndicator(dir, 4) {
			return true
		}
	}
	return false
}

func isStrong1CFileName(name string) bool {
	return strings.EqualFold(name, "Configuration.xml") || strings.EqualFold(name, "ConfigDumpInfo.xml")
}

func isStrong1CExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".bsl", ".cf", ".cfe", ".epf", ".erf":
		return true
	}
	return false
}

func isStrong1CDirectoryName(name string) bool {
	for _, strong := range []string{"Catalogs", "Documents", "CommonModules", "InformationRegisters", "AccumulationRegisters"} {
		if strings.EqualFold(name, strong) {
			return true
		}
	}
	return false
}

// containsStrong1CIndicator reports whether a strongly indicative 1C file
// exists up to depth directory levels below start, mirroring
// Get-ChildItem -Recurse -Depth 4 in Test-Confirmed1CProject; unreadable
// directories are skipped like -ErrorAction SilentlyContinue.
func containsStrong1CIndicator(start string, depth int) bool {
	entries, err := os.ReadDir(start)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if isStrong1CFileName(entry.Name()) || isStrong1CExtension(filepath.Ext(entry.Name())) {
				return true
			}
			continue
		}
		if depth > 0 && containsStrong1CIndicator(filepath.Join(start, entry.Name()), depth-1) {
			return true
		}
	}
	return false
}

// enclosingGitRepo walks up from root to the nearest directory holding a
// .git entry (repository directory or worktree pointer file). It is the
// dependency-free port of the `git rev-parse --show-toplevel` probe in
// Initialize-BSLFlowProject.ps1:169-178. Divergence: the PS probe resolves
// through the git binary (symlinked work trees, GIT_DIR); the filesystem
// walk is exact for ordinary clones and worktrees.
func enclosingGitRepo(root string) (string, bool) {
	dir := root
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// updatedSentinelVersion ports Set-SentinelVersion
// (Update-BSLFlowProject.ps1:143-148) onto byte splices: the first
// framework_version line keeps its indentation and receives the quoted
// target; with no such line the pair is appended after the trimmed text
// with the file's dominant newline; duplicates are an error.
func updatedSentinelVersion(text, version string) (string, error) {
	matches := sentinelVersionLine.FindAllStringSubmatchIndex(text, -1)
	if len(matches) > 1 {
		return "", errors.New("Project sentinel contains duplicate framework_version keys.")
	}
	if len(matches) == 0 {
		newline := detectNewline(text)
		return strings.TrimRight(text, "\r\n") + newline + "framework_version: \"" + version + "\"" + newline, nil
	}
	loc := matches[0]
	// loc[2] is where the value starts: everything before it is the PS
	// replace pattern's $1 group (indentation, key, spacing).
	return text[:loc[2]] + "\"" + version + "\"" + text[loc[1]:], nil
}

// bfVersion ports Convert-BFVersion (Update-BSLFlowProject.ps1:20-29);
// numbers are parsed as 32-bit ints exactly like the PS [int] casts.
type bfVersion struct {
	numbers    [3]int
	prerelease []string
}

func parseBFVersion(value string) (bfVersion, error) {
	match := bfVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return bfVersion{}, fmt.Errorf("Unsupported framework_version '%s'.", value)
	}
	var parsed bfVersion
	for i := 0; i < 3; i++ {
		if match[i+1] == "" {
			continue
		}
		number, err := strconv.ParseInt(match[i+1], 10, 32)
		if err != nil {
			return bfVersion{}, fmt.Errorf("Unsupported framework_version '%s'.", value)
		}
		parsed.numbers[i] = int(number)
	}
	if match[4] != "" {
		parsed.prerelease = strings.Split(match[4], ".")
	}
	return parsed, nil
}

// compareBFVersion ports Compare-BFVersion (Update-BSLFlowProject.ps1:31-62):
// numeric fields first, then semver-style prerelease ordering where a
// release sorts above any prerelease, numeric parts sort below alphanumeric
// ones, and ties compare ordinally.
func compareBFVersion(left, right string) (int, error) {
	a, err := parseBFVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseBFVersion(right)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if a.numbers[i] < b.numbers[i] {
			return -1, nil
		}
		if a.numbers[i] > b.numbers[i] {
			return 1, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) > 0 {
		return 1, nil
	}
	if len(a.prerelease) > 0 && len(b.prerelease) == 0 {
		return -1, nil
	}
	count := len(a.prerelease)
	if len(b.prerelease) > count {
		count = len(b.prerelease)
	}
	for i := 0; i < count; i++ {
		if i >= len(a.prerelease) {
			return -1, nil
		}
		if i >= len(b.prerelease) {
			return 1, nil
		}
		leftPart, rightPart := a.prerelease[i], b.prerelease[i]
		leftNumber, leftNumeric := parseInt32(leftPart)
		rightNumber, rightNumeric := parseInt32(rightPart)
		switch {
		case leftNumeric && rightNumeric:
			if leftNumber != rightNumber {
				if leftNumber < rightNumber {
					return -1, nil
				}
				return 1, nil
			}
		case leftNumeric != rightNumeric:
			if leftNumeric {
				return -1, nil
			}
			return 1, nil
		default:
			if comparison := strings.Compare(leftPart, rightPart); comparison != 0 {
				return comparison, nil
			}
		}
	}
	return 0, nil
}

func parseInt32(text string) (int64, bool) {
	number, err := strconv.ParseInt(text, 10, 32)
	return number, err == nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
