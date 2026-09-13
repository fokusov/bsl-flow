package worker

import (
	"bytes"
	"testing"
)

func strictReceiptFixture() HostResult {
	return HostResult{
		SessionID:       rolloutFixtureSession,
		RequestedModel:  "gpt-5.6-luna",
		RequestedEffort: "high",
		ObservedModel:   "gpt-5.6-luna",
		ObservedEffort:  "high",
		IdentitySource:  IdentitySourceRollout,
		Usage:           &Usage{InputTokens: 4, CachedInputTokens: 0, OutputTokens: 2},
		UsageSource:     "C:/task/stdout.txt",
		RolloutPath:     "C:/codex/sessions/2026/09/12/rollout-x.jsonl",
		TurnID:          "fixture-turn",
		Status:          StatusCompleted,
		ExitCode:        0,
		Summary:         "fixture",
	}
}

func TestHostResultCanonicalStrictReceipt(t *testing.T) {
	receipt := strictReceiptFixture()
	first, err := receipt.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	second, err := receipt.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical encoding is not deterministic:\n%s\n%s", first, second)
	}
	expected := `{"exit_code":0,"observed_effort":"high","observed_model":"gpt-5.6-luna","requested_effort":"high","requested_model":"gpt-5.6-luna","rollout_path":"C:/codex/sessions/2026/09/12/rollout-x.jsonl","session_id":"` +
		rolloutFixtureSession + `","status":"completed","summary":"fixture","turn_id":"fixture-turn","usage":{"cached_input_tokens":0,"input_tokens":4,"output_tokens":2},"usage_source":"C:/task/stdout.txt"}`
	if string(first) != expected {
		t.Fatalf("canonical receipt mismatch:\n got %s\nwant %s", first, expected)
	}
}

func TestHostResultCanonicalEphemeralReceipt(t *testing.T) {
	// Ordinary ephemeral workers keep the v1 shape: nullable identity, no
	// rollout provenance members, optional usage counters retained
	// (Codex.ps1:99-101).
	cacheWrite, reasoning := int64(0), int64(1)
	receipt := HostResult{
		SessionID:       rolloutFixtureSession,
		RequestedModel:  "gpt-5.6-sol",
		RequestedEffort: "medium",
		Usage:           &Usage{InputTokens: 4, CachedInputTokens: 0, OutputTokens: 2, CacheWriteInputTokens: &cacheWrite, ReasoningOutputTokens: &reasoning},
		UsageSource:     "C:/task/stdout.txt",
		Status:          StatusNeedsInput,
		ExitCode:        0,
		Summary:         "awaiting input",
	}
	encoded, err := receipt.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"exit_code":0,"observed_effort":null,"observed_model":null,"requested_effort":"medium","requested_model":"gpt-5.6-sol","session_id":"` +
		rolloutFixtureSession + `","status":"needs_input","summary":"awaiting input","usage":{"cache_write_input_tokens":0,"cached_input_tokens":0,"input_tokens":4,"output_tokens":2,"reasoning_output_tokens":1},"usage_source":"C:/task/stdout.txt"}`
	if string(encoded) != expected {
		t.Fatalf("ephemeral receipt mismatch:\n got %s\nwant %s", encoded, expected)
	}
}

func TestHostResultCanonicalNilUsageAndEscaping(t *testing.T) {
	receipt := HostResult{Status: StatusFailed, ExitCode: 1, Summary: "line\nbreak \"quoted\""}
	encoded, err := receipt.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"exit_code":1,"observed_effort":null,"observed_model":null,"requested_effort":null,"requested_model":null,"session_id":"","status":"failed","summary":"line\nbreak \"quoted\"","usage":null,"usage_source":null}`
	if string(encoded) != expected {
		t.Fatalf("minimal receipt mismatch:\n got %s\nwant %s", encoded, expected)
	}
}

func TestHostResultCanonicalRejectsInvalidUTF8(t *testing.T) {
	receipt := strictReceiptFixture()
	receipt.Summary = "invalid \xffutf8"
	if _, err := receipt.Canonical(); err == nil {
		t.Fatal("invalid UTF-8 receipt member must be rejected")
	}
}

func TestValidateObserved(t *testing.T) {
	cases := []struct {
		name             string
		source           string
		observedModel    string
		observedEffort   string
		requestedModel   string
		requestedEffort  string
		wantErr          bool
		wantClassBlocked bool
	}{
		{
			name:            "rollout source with resolved identity",
			source:          IdentitySourceRollout,
			observedModel:   "gpt-5.6-luna",
			observedEffort:  "high",
			requestedModel:  "gpt-5.6-luna",
			requestedEffort: "high",
		},
		{
			name:           "provider envelope source",
			source:         IdentitySourceProvider,
			observedModel:  "gpt-6-astra",
			observedEffort: "xhigh",
		},
		{
			name:             "empty observed model rejected",
			source:           IdentitySourceRollout,
			observedEffort:   "high",
			wantErr:          true,
			wantClassBlocked: true,
		},
		{
			name:             "empty observed effort rejected",
			source:           IdentitySourceRollout,
			observedModel:    "gpt-5.6-luna",
			wantErr:          true,
			wantClassBlocked: true,
		},
		{
			name:             "request echo without evidence source rejected",
			observedModel:    "gpt-5.6-luna",
			observedEffort:   "high",
			requestedModel:   "gpt-5.6-luna",
			requestedEffort:  "high",
			wantErr:          true,
			wantClassBlocked: true,
		},
		{
			name:             "unknown evidence source rejected",
			source:           "request_values",
			observedModel:    "gpt-5.6-luna",
			observedEffort:   "high",
			wantErr:          true,
			wantClassBlocked: true,
		},
		{
			name:             "observed model differing from strict request rejected",
			source:           IdentitySourceRollout,
			observedModel:    "gpt-6-astra",
			observedEffort:   "high",
			requestedModel:   "gpt-5.6-luna",
			requestedEffort:  "high",
			wantErr:          true,
			wantClassBlocked: true,
		},
		{
			name:             "observed effort differing from strict request rejected",
			source:           IdentitySourceProvider,
			observedModel:    "gpt-5.6-luna",
			observedEffort:   "low",
			requestedModel:   "gpt-5.6-luna",
			requestedEffort:  "high",
			wantErr:          true,
			wantClassBlocked: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			receipt := HostResult{
				ObservedModel:  test.observedModel,
				ObservedEffort: test.observedEffort,
				IdentitySource: test.source,
			}
			err := ValidateObserved(receipt, test.requestedModel, test.requestedEffort)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected validation failure")
				}
				if test.wantClassBlocked && ClassOf(err) != "BF_BLOCKED" {
					t.Fatalf("identity refusal must be BF_BLOCKED, got %q: %v", ClassOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected validation failure: %v", err)
			}
		})
	}
}
