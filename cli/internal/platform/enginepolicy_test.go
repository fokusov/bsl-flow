package platform

import "testing"

func TestParseEngineClosedSet(t *testing.T) {
	for _, value := range []string{"native", "legacy-powershell"} {
		engine, err := ParseEngine(value)
		if err != nil {
			t.Fatalf("engine %q rejected: %v", value, err)
		}
		if engine != Engine(value) {
			t.Fatalf("engine %q parsed as %q", value, engine)
		}
	}
	for _, value := range []string{"", "Native", "powershell", "legacy", "native ", "native\n", "powershell-legacy", "auto"} {
		if engine, err := ParseEngine(value); err == nil {
			t.Fatalf("engine %q accepted as %q", value, engine)
		}
	}
}

func TestDecideEngineMatrix(t *testing.T) {
	const legacyStoreReason = "legacy-powershell serves only checkout-local v1"
	cases := []struct {
		name    string
		goos    string
		engine  Engine
		store   StoreSchema
		allowed bool
		reason  string
		wantErr bool
	}{
		{"native v1 windows", "windows", EngineNative, StoreCheckoutLocalV1, true, "", false},
		{"native v2 windows", "windows", EngineNative, StoreRepositoryV2, true, "", false},
		{"native v1 darwin", "darwin", EngineNative, StoreCheckoutLocalV1, true, "", false},
		{"native v2 darwin", "darwin", EngineNative, StoreRepositoryV2, true, "", false},
		{"native v1 linux", "linux", EngineNative, StoreCheckoutLocalV1, true, "", false},
		{"native v2 linux", "linux", EngineNative, StoreRepositoryV2, true, "", false},
		{"legacy v1 windows", "windows", EngineLegacyPowerShell, StoreCheckoutLocalV1, true, "", false},
		{"legacy v2 windows", "windows", EngineLegacyPowerShell, StoreRepositoryV2, false, legacyStoreReason, false},
		{"legacy v1 darwin", "darwin", EngineLegacyPowerShell, StoreCheckoutLocalV1, false, "unsupported platform", false},
		{"legacy v1 linux", "linux", EngineLegacyPowerShell, StoreCheckoutLocalV1, false, "unsupported platform", false},
		{"legacy v2 darwin", "darwin", EngineLegacyPowerShell, StoreRepositoryV2, false, "unsupported platform", false},
		{"legacy v2 linux", "linux", EngineLegacyPowerShell, StoreRepositoryV2, false, "unsupported platform", false},
		{"unknown engine", "windows", Engine("codex"), StoreCheckoutLocalV1, false, "", true},
		{"empty engine", "windows", Engine(""), StoreCheckoutLocalV1, false, "", true},
		{"unknown store", "windows", EngineNative, StoreSchema(99), false, "", true},
		{"zero store", "windows", EngineNative, StoreSchema(0), false, "", true},
		{"empty goos", "", EngineNative, StoreCheckoutLocalV1, false, "", true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			decision, err := DecideEngine(item.goos, item.engine, item.store)
			if item.wantErr {
				if err == nil {
					t.Fatalf("decision accepted: %+v", decision)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != item.allowed {
				t.Fatalf("allowed = %v, want %v (reason %q)", decision.Allowed, item.allowed, decision.Reason)
			}
			if decision.Reason != item.reason {
				t.Fatalf("reason = %q, want %q", decision.Reason, item.reason)
			}
		})
	}
}

func TestEnginesForGOOS(t *testing.T) {
	cases := []struct {
		goos    string
		engines []string
	}{
		{"windows", []string{string(EngineNative), string(EngineLegacyPowerShell)}},
		{"darwin", []string{string(EngineNative)}},
		{"linux", []string{string(EngineNative)}},
	}
	for _, item := range cases {
		got := EnginesForGOOS(item.goos)
		if len(got) != len(item.engines) {
			t.Fatalf("engines for %s = %v, want %v", item.goos, got, item.engines)
		}
		for i := range got {
			if string(got[i]) != item.engines[i] {
				t.Fatalf("engines for %s = %v, want %v", item.goos, got, item.engines)
			}
		}
	}
}
