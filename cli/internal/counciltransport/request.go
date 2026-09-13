package counciltransport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Wire contract of Get-BSLFlowCouncilApiRequest (Council.Transport.ps1:190-197):
// model, exactly one user message, response_format json_object, and either
// reasoning_effort (string effort) or max_tokens (integer effort). The legacy
// transport sends no stream and no temperature field.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireResponseFormat struct {
	Type string `json:"type"`
}

type wireRequest struct {
	Model           string             `json:"model"`
	Messages        []wireMessage      `json:"messages"`
	ResponseFormat  wireResponseFormat `json:"response_format"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	MaxTokens       int                `json:"max_tokens,omitempty"`
}

// Request is the closed openai_compatible chat request envelope. Effort must be
// one of low/medium/high/xhigh or a positive integer token budget.
type Request struct {
	Model  string
	Prompt string
	Effort string
}

func (r Request) Canonical() ([]byte, error) {
	wire, err := r.wire()
	if err != nil {
		return nil, err
	}
	return marshalDeterministic(wire)
}

func (r Request) wire() (*wireRequest, error) {
	model := strings.TrimSpace(r.Model)
	if model == "" {
		return nil, fmt.Errorf("counciltransport: request model is empty")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return nil, fmt.Errorf("counciltransport: request prompt is empty")
	}
	effort := strings.TrimSpace(r.Effort)
	if effort == "" {
		return nil, fmt.Errorf("counciltransport: council effort is empty; the caller applies the medium default")
	}
	wire := &wireRequest{
		Model:           model,
		Messages:        []wireMessage{{Role: "user", Content: r.Prompt}},
		ResponseFormat:  wireResponseFormat{Type: "json_object"},
		ReasoningEffort: effort,
	}
	if !reasoningEfforts[effort] {
		tokenBudget, err := strconv.Atoi(effort)
		if err != nil || tokenBudget < 1 {
			return nil, fmt.Errorf("counciltransport: unsupported council effort: %s", effort)
		}
		wire.ReasoningEffort = ""
		wire.MaxTokens = tokenBudget
	}
	return wire, nil
}

var reasoningEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
}

// Credentials carry the provider bearer token. The key is never logged and
// never serialized into request or member envelopes.
type Credentials struct {
	APIKey string
}

func (c Credentials) String() string { return "Credentials{APIKey:<redacted>}" }

func (c Credentials) GoString() string { return c.String() }

func (c Credentials) MarshalJSON() ([]byte, error) { return []byte(`"<redacted>"`), nil }

// marshalDeterministic emits compact JSON with struct fields in declaration
// order and raw UTF-8 (no HTML escaping), so bytes are stable across calls.
func marshalDeterministic(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
