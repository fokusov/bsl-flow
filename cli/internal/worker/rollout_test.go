package worker

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const rolloutFixtureSession = "11111111-1111-4111-8111-111111111111"

func rolloutLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func sessionMetaLine(sessionID string) string {
	return `{"timestamp":"2026-09-12T00:00:00Z","ordinal":0,"type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","originator":"codex_exec","cli_version":"0.154.0"}}`
}

func turnContextLine(model, effort string) string {
	return `{"timestamp":"2026-09-12T00:00:01Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"fixture-turn","model":"` + model + `","effort":"` + effort + `","approval_policy":"never"}}`
}

func TestParseRolloutSessionFixtureExtractsIdentity(t *testing.T) {
	data, err := os.ReadFile("testdata/rollout-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ParseRolloutSession(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("valid rollout fixture rejected: %v", err)
	}
	if identity.Model != "gpt-5.6-sol" || identity.Effort != "medium" || identity.SessionID != rolloutFixtureSession {
		t.Fatalf("identity not extracted from rollout: %+v", identity)
	}
}

func TestParseRolloutSessionCases(t *testing.T) {
	cases := []struct {
		name    string
		rollout string
		want    RolloutIdentity
		wantErr error
	}{
		{
			name:    "single turn context",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), turnContextLine("gpt-5.6-sol", "medium")),
			want:    RolloutIdentity{Model: "gpt-5.6-sol", Effort: "medium", SessionID: rolloutFixtureSession},
		},
		{
			name:    "blank lines skipped and unknown kinds tolerated",
			rollout: rolloutLines("", sessionMetaLine(rolloutFixtureSession), "   ", `{"type":"event_item","payload":{"future":true}}`, turnContextLine("gpt-5.6-sol", "medium")),
			want:    RolloutIdentity{Model: "gpt-5.6-sol", Effort: "medium", SessionID: rolloutFixtureSession},
		},
		{
			name:    "legacy session meta exposes only id",
			rollout: rolloutLines(`{"timestamp":"2020-01-01T00:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"01234567-89ab-4cde-8123-0123456789ab","originator":"codex_exec","cli_version":"0.154.0"}}`, `{"timestamp":"2020-01-01T00:00:01Z","ordinal":1,"type":"turn_context","payload":{"model":"legacy-model","effort":"low"}}`),
			want:    RolloutIdentity{Model: "legacy-model", Effort: "low", SessionID: "01234567-89ab-4cde-8123-0123456789ab"},
		},
		{
			name:    "two turn contexts are ambiguous",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), turnContextLine("gpt-5.6-sol", "medium"), turnContextLine("gpt-5.6-sol", "high")),
			wantErr: ErrRolloutAmbiguousTurn,
		},
		{
			name:    "no turn context is missing identity",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession)),
			wantErr: ErrRolloutMissingTurn,
		},
		{
			name:    "null payload turn context is not usable identity",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), `{"timestamp":"2026-09-12T00:00:01Z","ordinal":1,"type":"turn_context","payload":null}`),
			wantErr: ErrRolloutMissingTurn,
		},
		{
			name:    "usable turn context without model is incomplete",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), `{"type":"turn_context","payload":{"turn_id":"fixture-turn","effort":"medium"}}`),
			wantErr: ErrRolloutIncompleteIdentity,
		},
		{
			name:    "usable turn context without effort is incomplete",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), `{"type":"turn_context","payload":{"turn_id":"fixture-turn","model":"gpt-5.6-sol"}}`),
			wantErr: ErrRolloutIncompleteIdentity,
		},
		{
			name:    "invalid json line is rejected",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), "broken json"),
			wantErr: ErrRolloutMalformed,
		},
		{
			name:    "duplicate session metadata is rejected",
			rollout: rolloutLines(sessionMetaLine(rolloutFixtureSession), sessionMetaLine(rolloutFixtureSession), turnContextLine("gpt-5.6-sol", "medium")),
			wantErr: ErrRolloutSessionMeta,
		},
		{
			name:    "session meta without any id is rejected",
			rollout: rolloutLines(`{"type":"session_meta","payload":{"originator":"codex_exec"}}`, turnContextLine("gpt-5.6-sol", "medium")),
			wantErr: ErrRolloutSessionMeta,
		},
		{
			name:    "missing session meta is rejected",
			rollout: rolloutLines(turnContextLine("gpt-5.6-sol", "medium")),
			wantErr: ErrRolloutSessionMeta,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			identity, err := ParseRolloutSession(strings.NewReader(test.rollout))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("expected sentinel %v, got %v", test.wantErr, err)
				}
				if ClassOf(err) != "BF_BLOCKED" {
					t.Fatalf("rollout provenance refusal must be BF_BLOCKED, got %q: %v", ClassOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if identity != test.want {
				t.Fatalf("identity mismatch: got %+v, want %+v", identity, test.want)
			}
		})
	}
}

func TestParseRolloutSessionEnforcesByteBound(t *testing.T) {
	oversized := sessionMetaLine(rolloutFixtureSession) + "\n" +
		`{"type":"turn_context","payload":{"model":"` + strings.Repeat("a", MaxRolloutBytes+1) + `","effort":"medium"}}` + "\n"
	_, err := ParseRolloutSession(strings.NewReader(oversized))
	if !errors.Is(err, ErrRolloutTooLarge) {
		t.Fatalf("oversized record must hit the byte bound, got %v", err)
	}
}

func TestParseRolloutSessionEnforcesLineBound(t *testing.T) {
	rollout := strings.Repeat(`{"type":"event_item"}`+"\n", MaxRolloutLines+1)
	_, err := ParseRolloutSession(strings.NewReader(rollout))
	if !errors.Is(err, ErrRolloutTooLarge) {
		t.Fatalf("record count must hit the line bound, got %v", err)
	}
}
