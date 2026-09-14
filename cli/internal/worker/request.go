package worker

import (
	"path/filepath"
	"strings"
)

// Shared request data of the managed worker adapters: the sealed execution
// profile and model selection of the controller request
// (Assert-BFExecutionProfile, Task.Execution.ps1:21-51).

// SandboxIdentity is the pinned sandbox executable identity.
type SandboxIdentity struct {
	Executable string
	SHA256     string
}

// ToolsetIdentity is the pinned toolset snapshot identity.
type ToolsetIdentity struct {
	Name   string // "unica" | "cc-1c-skills"
	Root   string
	SHA256 string
}

// RuntimePackage is one pinned interpreter package of a cc-1c-skills runtime.
type RuntimePackage struct {
	Name    string
	Version string
}

// RuntimePin is the pinned Python interpreter contract of a cc-1c-skills
// toolset (Assert-BFRuntimePin, Task.Execution.ps1:5-19).
type RuntimePin struct {
	Executable string
	SHA256     string
	Version    string
	Packages   []RuntimePackage
}

// UnicaIdentity is the exact Unica source tool contract.
type UnicaIdentity struct {
	PluginRoot      string
	BootstrapSHA256 string
	ManifestSHA256  string
	RuntimeCache    string
	AllowedTools    []string
}

// ExecutionProfile mirrors the sealed execution_profile of the controller
// request. Every member is supplied by trusted host state, never by provider
// JSON input.
type ExecutionProfile struct {
	Provider          string // "codex" | "opencode"
	Executable        string
	ExecutableSHA256  string
	Sandbox           SandboxIdentity
	Toolset           ToolsetIdentity
	DeniedReadRoots   []string
	Unica             *UnicaIdentity
	Runtime           *RuntimePin
	CodexSkillsSHA256 string
}

// WorkerModels mirrors request.models: the worker and reviewer model/effort
// pairs. An empty effort is the PowerShell null ("no override").
type WorkerModels struct {
	Worker         string
	WorkerEffort   string
	Reviewer       string
	ReviewerEffort string
}

// Stages that select the reviewer model instead of the worker model
// (Invoke-BFProfiledCodexWorker, ProfiledCodex.ps1:206).
const (
	StageImplement  = "implement"
	StageCodeReview = "code_review"
	StageSpecReview = "spec_review"
)

func isReviewStage(stage string) bool {
	return stage == StageCodeReview || stage == StageSpecReview
}

// Selection resolves the model/effort pair for a stage.
func (m WorkerModels) Selection(stage string) (model string, effort string) {
	if isReviewStage(stage) {
		return m.Reviewer, m.ReviewerEffort
	}
	return m.Worker, m.WorkerEffort
}

// ToolsetPrompt mirrors Get-BFToolsetPrompt (Task.Execution.ps1:442-455): the
// guidance text appended to every non-critic managed dispatch. It is
// deterministic guidance only — the interpreter pin itself is enforced by the
// controller, not by this text.
func ToolsetPrompt(profile ExecutionProfile) (string, error) {
	manifest, err := readJSONObjectFile(filepath.Join(profile.Toolset.Root, "toolset-manifest.json"))
	if err != nil {
		return "", err
	}
	skills, _ := asArray(manifest["skills"])
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		skillObject, ok := asObject(skill)
		if !ok {
			continue
		}
		if name, ok := asString(skillObject["name"]); ok {
			names = append(names, name)
		}
	}
	catalog := strings.Join(names, ", ")
	runtimeNote := ""
	if profile.Toolset.Name == "cc-1c-skills" {
		packages := make([]string, 0, len(profile.Runtime.Packages))
		for _, pkg := range profile.Runtime.Packages {
			packages = append(packages, pkg.Name+"=="+pkg.Version)
		}
		runtimeNote = " Use only this pinned Python interpreter for skill scripts: " + profile.Runtime.Executable +
			" (Python " + profile.Runtime.Version + "; required: " + strings.Join(packages, ", ") +
			"). Do not call another python, search PATH or install packages."
	}
	return "\nSelected 1C toolset: " + profile.Toolset.Name +
		". Read only the relevant SKILL.md under this fixed root: " + profile.Toolset.Root +
		". Skills: " + catalog +
		". Legacy relative .opencode/skills paths in these instructions refer to this selected root; use its absolute paths. Do not load any other skillset or install dependencies." +
		runtimeNote +
		" Runtime/database/build/test actions remain controller-owned; tool availability is not runtime authorization.", nil
}
