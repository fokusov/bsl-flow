package worker

import (
	"context"
	"time"
)

// Port of Invoke-BFManagedWorker (Task.Execution.ps1:565-583): the shared
// pre-dispatch gates and the provider selection of the managed worker route.
// The BFI-003/BFI-005 gates (runtime preflight, budget admission/reservation)
// run on this shared path so both providers receive the same runtime and
// budget contract.

// BudgetHooks are the controller budget seams of the dispatch: admission
// (Assert-BFBudgetAdmission / Assert-BFProviderBudgetAdmission), reservation
// (Add-BFBudgetReservation / Add-BFProviderBudgetReservation) and completion
// (Complete-BFBudgetDispatch / Complete-BFProviderBudgetDispatch). Their
// ledger semantics stay with the controller state machine; the dispatch only
// preserves the exact call order. A nil Budget means the request carries no
// budget (Test-BFCoverageProperty 'budget' false), matching the PowerShell
// guards that return early without a budget.
type BudgetHooks struct {
	Admit    func(ctx context.Context, stage, model string) error
	Reserve  func(ctx context.Context, stage, model string) error
	Complete func(ctx context.Context, stage, model string) error
}

// ManagedWorkerRequest is the Invoke-BFManagedWorker parameter surface plus
// the controller seams of both provider adapters.
type ManagedWorkerRequest struct {
	Stage          string
	Prompt         string
	Directory      string
	CodexPath      string
	MaxOutputBytes int64 // 0 selects the 16777216 default
	WorkerPath     string
	ProjectPath    string
	TaskID         string
	TimeoutSeconds int
	Profile        ExecutionProfile
	Models         WorkerModels
	Deadline       time.Time

	// RuntimePreflight mirrors Test-BFRuntimePreflight (BFI-003: only the
	// exact pinned interpreter before a paid dispatch).
	RuntimePreflight func(directory string) error
	// Budget carries the request budget contract; nil means no budget.
	Budget *BudgetHooks

	// Provider adapter seams, forwarded verbatim.
	RequireObservedIdentity   bool
	FallbackCatalogSourcePath string
	FallbackCatalogSHA256     string
	SchemaPath                string
	AdapterSHA256             string
	RpcSHA256                 string
	CodexHome                 string
	UserProfile               string
	Dependencies              func() (map[string]any, error)
	PermissionProfile         func(scratch, config string, writable bool) (string, error)
	TestExecutionCapability   func(capabilityDir, scratch, config, permissions string, writable bool) error
}

// WorkerOutcome is the provider-independent terminal result of a managed
// dispatch: the sealed worker result consolidated with receipt metadata.
type WorkerOutcome struct {
	Provider    string
	Status      string
	Summary     string
	PayloadJSON string
	Raw         map[string]any
	HostResult  map[string]any
	SessionID   string
}

// RunManagedWorker mirrors Invoke-BFManagedWorker: runtime preflight, budget
// admission and reservation, provider dispatch (opencode vs profiled codex)
// and budget completion on success. Like the PowerShell original it does not
// complete the budget when the worker itself refuses.
func RunManagedWorker(ctx context.Context, req ManagedWorkerRequest) (WorkerOutcome, error) {
	if req.MaxOutputBytes == 0 {
		req.MaxOutputBytes = 16777216
	}
	if req.Profile.Provider == "" {
		// The unmanaged Invoke-BFCodexWorker route (no execution_profile) is
		// intentionally not ported here: the native CLI dispatches only
		// managed profiles.
		return WorkerOutcome{}, invalid("managed worker dispatch requires an execution profile.")
	}
	if req.RuntimePreflight != nil {
		if err := req.RuntimePreflight(req.Directory); err != nil {
			return WorkerOutcome{}, err
		}
	}
	requestedModel, _ := req.Models.Selection(req.Stage)
	hasBudget := req.Budget != nil
	if hasBudget {
		if req.Budget.Admit != nil {
			if err := req.Budget.Admit(ctx, req.Stage, requestedModel); err != nil {
				return WorkerOutcome{}, err
			}
		}
		if req.Budget.Reserve != nil {
			if err := req.Budget.Reserve(ctx, req.Stage, requestedModel); err != nil {
				return WorkerOutcome{}, err
			}
		}
	}
	var outcome WorkerOutcome
	if req.Profile.Provider == "opencode" {
		result, err := RunOpenCode(ctx, OpenCodeRequest{
			Stage:                   req.Stage,
			Prompt:                  req.Prompt,
			Directory:               req.Directory,
			CodexPath:               req.CodexPath,
			MaxOutputBytes:          req.MaxOutputBytes,
			WorkerPath:              req.WorkerPath,
			ProjectPath:             req.ProjectPath,
			TaskID:                  req.TaskID,
			TimeoutSeconds:          req.TimeoutSeconds,
			Profile:                 req.Profile,
			Models:                  req.Models,
			UserProfile:             req.UserProfile,
			Dependencies:            req.Dependencies,
			PermissionProfile:       req.PermissionProfile,
			TestExecutionCapability: req.TestExecutionCapability,
			Deadline:                req.Deadline,
		})
		if err != nil {
			return WorkerOutcome{}, err
		}
		outcome = WorkerOutcome{
			Provider:    "opencode",
			Status:      result.Status,
			Summary:     result.Summary,
			PayloadJSON: result.PayloadJSON,
			Raw:         result.Raw,
			HostResult:  result.HostResult,
			SessionID:   result.SessionID,
		}
	} else {
		result, err := RunProfiledCodex(ctx, ProfiledCodexRequest{
			Stage:                     req.Stage,
			Prompt:                    req.Prompt,
			Directory:                 req.Directory,
			CodexPath:                 req.CodexPath,
			MaxOutputBytes:            req.MaxOutputBytes,
			WorkerPath:                req.WorkerPath,
			ProjectPath:               req.ProjectPath,
			TaskID:                    req.TaskID,
			TimeoutSeconds:            req.TimeoutSeconds,
			Profile:                   req.Profile,
			Models:                    req.Models,
			RequireObservedIdentity:   req.RequireObservedIdentity,
			FallbackCatalogSourcePath: req.FallbackCatalogSourcePath,
			FallbackCatalogSHA256:     req.FallbackCatalogSHA256,
			SchemaPath:                req.SchemaPath,
			AdapterSHA256:             req.AdapterSHA256,
			RpcSHA256:                 req.RpcSHA256,
			CodexHome:                 req.CodexHome,
			Dependencies:              req.Dependencies,
			PermissionProfile:         req.PermissionProfile,
			TestExecutionCapability:   req.TestExecutionCapability,
			Deadline:                  req.Deadline,
		})
		if err != nil {
			return WorkerOutcome{}, err
		}
		outcome = WorkerOutcome{
			Provider:    "codex",
			Status:      result.Status,
			Summary:     result.Summary,
			PayloadJSON: result.PayloadJSON,
			Raw:         result.Raw,
			HostResult:  result.HostResult,
			SessionID:   result.SessionID,
		}
	}
	if hasBudget && req.Budget.Complete != nil {
		if err := req.Budget.Complete(ctx, req.Stage, requestedModel); err != nil {
			return WorkerOutcome{}, err
		}
	}
	return outcome, nil
}
