package platform

import "fmt"

// Engine is the closed engine set of the transition policy. Only the two
// constants below are valid; ParseEngine rejects every other string.
type Engine string

const (
	EngineNative           Engine = "native"
	EngineLegacyPowerShell Engine = "legacy-powershell"
)

func ParseEngine(value string) (Engine, error) {
	switch Engine(value) {
	case EngineNative, EngineLegacyPowerShell:
		return Engine(value), nil
	default:
		return "", fmt.Errorf("unknown engine %q", value)
	}
}

// StoreSchema identifies the task store contract a request targets.
type StoreSchema int

const (
	StoreCheckoutLocalV1 StoreSchema = iota + 1
	StoreRepositoryV2
)

// Decision states whether an engine may serve a request; Reason is set for
// every denial and empty for every allowance.
type Decision struct {
	Allowed bool
	Reason  string
}

// EnginesForGOOS lists the valid engines for the platform: the legacy engine
// exists only on windows.
func EnginesForGOOS(goos string) []Engine {
	if goos == "windows" {
		return []Engine{EngineNative, EngineLegacyPowerShell}
	}
	return []Engine{EngineNative}
}

// DecideEngine applies the transition compatibility matrix. The legacy engine
// serves only checkout-local v1 and only on windows; the repository v2 store
// is written by the native engine on every platform.
//
// Contract: a native engine failure must never auto-fallback to
// legacy-powershell. Fallback hides defects and can duplicate external
// effects; callers surface the failure as a typed error instead.
func DecideEngine(goos string, engine Engine, store StoreSchema) (Decision, error) {
	if goos == "" {
		return Decision{}, fmt.Errorf("unknown operating system %q", goos)
	}
	switch engine {
	case EngineNative, EngineLegacyPowerShell:
	default:
		return Decision{}, fmt.Errorf("unknown engine %q", string(engine))
	}
	switch store {
	case StoreCheckoutLocalV1, StoreRepositoryV2:
	default:
		return Decision{}, fmt.Errorf("unknown store schema %d", int(store))
	}
	if engine == EngineLegacyPowerShell {
		if goos != "windows" {
			return Decision{Allowed: false, Reason: "unsupported platform"}, nil
		}
		if store != StoreCheckoutLocalV1 {
			return Decision{Allowed: false, Reason: "legacy-powershell serves only checkout-local v1"}, nil
		}
	}
	return Decision{Allowed: true}, nil
}
