package repository

import (
	"context"
	"strings"
)

// NativeProviderContract is the closed contract identity exchanged between
// the Go controller and a compatibility provider.  The controller persists
// the same values in the task's execution binding and never accepts a provider
// that silently changes identity.
const NativeProviderContract = "bsl-flow.native-provider.windows-ps.v1"

const nativeControllerSchema = int64(1)

// EngineIdentity identifies the executable host and the packaged provider.
// SHA-256 values are lowercase hexadecimal strings.  They are deliberately
// kept separate from the provider interface so registry reads can remain lazy.
type EngineIdentity struct {
	Name                string
	ContractVersion     int64
	Provider            string
	HostSHA256          string
	ProviderSHA256      string
	AssetManifestSHA256 string
	// These local paths are supplied by the trusted host, never by provider JSON.
	// The persisted engine remains the closed hash-only identity above.
	PolicyRoot string
	HostPath   string
}

// ProviderContractIdentity is the input/output identity used by the provider
// wire contract.  Provider contract version is named Version here because the
// JSON contract calls it `version`, while the persisted engine calls it
// `contract_version`.
type ProviderContractIdentity struct {
	Name                string `json:"name"`
	Version             int64  `json:"version"`
	HostSHA256          string `json:"host_sha256"`
	ProviderSHA256      string `json:"provider_sha256"`
	AssetManifestSHA256 string `json:"asset_manifest_sha256"`
}

// ArtifactRef is a provider-declared relative artifact and its immutable
// digest.  The controller resolves Path only below the registered artifact or
// context root.
type ArtifactRef struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Kind      string `json:"kind"`
}

// ProviderInput contains only the immutable, attempt-bound view needed by a
// provider.  It intentionally has no action, next_action, acceptance, or
// writable task-state field.  MeasureInput and ExecuteInput are aliases so a
// provider cannot accidentally receive a different shape for the two calls.
type ProviderInput struct {
	SchemaVersion      int64                    `json:"schema_version"`
	Contract           string                   `json:"contract"`
	Operation          string                   `json:"operation"`
	TaskID             string                   `json:"task_id"`
	StateView          map[string]any           `json:"state_view"`
	Attempt            map[string]any           `json:"attempt"`
	ContextRoot        string                   `json:"context_root"`
	ArtifactRoot       string                   `json:"artifact_root"`
	CanonicalStoreRoot string                   `json:"canonical_store_root"`
	CancelSignal       string                   `json:"cancel_signal"`
	ProviderContract   ProviderContractIdentity `json:"provider_contract"`
	PriorArtifacts     []ArtifactRef            `json:"prior_artifacts"`
	// Native1CCredential relays the private runtime auth of a native 1C
	// criterion. It travels only through the trusted provider input channel,
	// never through argv, logs or persisted evidence.
	Native1CCredential *Native1CRuntimeAuth `json:"native_1c_credential,omitempty"`
}

// Native1CRuntimeAuth is the private native 1C runtime credential pair.
type Native1CRuntimeAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ParseRuntimeAuthLine mirrors the -RuntimeAuth stdin contract of the legacy
// entrypoint: one private JSON line with username and password.
func ParseRuntimeAuthLine(line []byte) (*Native1CRuntimeAuth, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || len(trimmed) > 16384 {
		return nil, invalid("missing or oversized runtime auth input.")
	}
	object, err := DecodeObject([]byte(trimmed))
	if err != nil {
		return nil, invalid("malformed runtime auth input.")
	}
	auth, err := nativeObject(object, []string{"username", "password"}, nil, "runtime_auth")
	if err != nil {
		return nil, invalid("%v", err)
	}
	username := asStringOr(auth["username"])
	if strings.TrimSpace(username) == "" || len([]rune(username)) > 1024 {
		return nil, invalid("invalid runtime_auth.username.")
	}
	password, ok := auth["password"].(string)
	if !ok || len([]rune(password)) > 8192 {
		return nil, invalid("invalid runtime auth password.")
	}
	return &Native1CRuntimeAuth{Username: username, Password: password}, nil
}

type MeasureInput = ProviderInput
type ExecuteInput = ProviderInput

// Provider is the narrow Go controller ↔ provider seam.  Implementations may
// use ctx for process cancellation and deadlines, but receive no canonical
// store writer or controller command surface.
type Provider interface {
	Measure(ctx context.Context, input MeasureInput) (MeasureObservation, error)
	Execute(ctx context.Context, input ExecuteInput) (ExecuteObservation, error)
}

// MeasureObservation is a closed, read-only preflight result.  It does not
// authorize activation or dispatch; the controller decides what to persist.
type MeasureObservation struct {
	SchemaVersion  int64          `json:"schema_version"`
	Contract       string         `json:"contract"`
	TaskID         string         `json:"task_id"`
	Operation      string         `json:"operation"`
	RequestValid   bool           `json:"request_valid"`
	PolicyFiles    []any          `json:"policy_files"`
	PolicyRules    map[string]any `json:"policy_rules"`
	SourceManifest map[string]any `json:"source_manifest"`
	SpecInputs     map[string]any `json:"spec_inputs"`
	Dependencies   map[string]any `json:"dependencies"`
	Capability     map[string]any `json:"capability"`
	Blockers       []string       `json:"blockers"`
	// Transport is host-owned evidence for the provider process itself. It is
	// intentionally outside the wire observation and is retained separately by
	// the controller when a transport succeeds or fails.
	Transport *ProviderTransportEvidence `json:"-"`
}

// ExecuteObservation is a provider observation, not a controller result.  In
// particular, a provider cannot return a new revision, task status, next
// action, or acceptance receipt through this shape.
type ExecuteObservation struct {
	SchemaVersion    int64                    `json:"schema_version"`
	Contract         string                   `json:"contract"`
	TaskID           string                   `json:"task_id"`
	AttemptID        string                   `json:"attempt_id"`
	Stage            string                   `json:"stage"`
	Status           string                   `json:"status"`
	Summary          string                   `json:"summary"`
	Proposal         map[string]any           `json:"proposal"`
	SideEffects      string                   `json:"side_effects"`
	Dependencies     map[string]any           `json:"dependencies"`
	SourceManifest   map[string]any           `json:"source_manifest"`
	Artifacts        []ArtifactRef            `json:"artifacts"`
	ProcessReceipt   map[string]any           `json:"process_receipt"`
	ProviderContract ProviderContractIdentity `json:"provider_contract"`
	// Transport is host-owned evidence for the provider process itself; it is
	// never sent back to the provider as controller state.
	Transport *ProviderTransportEvidence `json:"-"`
}

// ProviderTransportEvidence contains the real outer process receipt captured
// by the host. Stdout/stderr are retained as bytes and hashed by the Go core;
// a provider must not manufacture an exit receipt after a transport failure.
type ProviderTransportEvidence struct {
	Receipt map[string]any
	Stdout  []byte
	Stderr  []byte
}

// ControllerHost may resolve the provider lazily.  The resolver is used only
// for activation and native execution; list/show/history/overview never invoke
// it and therefore do not require PowerShell, a bundle, or executable hashes.
type ControllerHost struct {
	Provider Provider
	Engine   EngineIdentity
	Resolve  func() (Provider, EngineIdentity, error)
	// RuntimeAuthReader reads the private runtime auth input lazily, only
	// when a dispatched stage actually needs the native 1C credential. The
	// legacy engine path never consumes it.
	RuntimeAuthReader func() (*Native1CRuntimeAuth, error)
	// Native1CRecovery carries the native recovery seams (journal root and
	// the COM control read). The production host wires the trusted local app
	// data journal and the in-binary inventory; tests substitute fixtures.
	Native1CRecovery *Native1CRecoveryRuntime
}

// NewControllerHost returns a host with an already available provider.  A
// caller that needs deferred executable/bundle setup can fill Resolve instead
// or construct ControllerHost directly.
func NewControllerHost(provider Provider, engine EngineIdentity) *ControllerHost {
	return &ControllerHost{Provider: provider, Engine: engine}
}
