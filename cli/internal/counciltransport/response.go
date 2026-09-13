package counciltransport

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Usage is the provider usage projection in council vocabulary. The mapping
// mirrors Register-BSLFlowCouncilMemberResult (Council.Engine.ps1:200-214):
// input side reads input_tokens then prompt_tokens; output side reads
// output_tokens then completion_tokens; reasoning reads reasoning_tokens then
// completion_tokens_details.reasoning_tokens. Unknown usage fields are
// tolerated.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

// Response is the parsed provider envelope. ObservedModel comes only from the
// response envelope .model field; a provider may alias or reroute the
// requested model, so the request model is never echoed here.
type Response struct {
	ObservedModel string
	Usage         Usage
	Content       string
}

// ParseResponse decodes one strict provider envelope: exactly one JSON object,
// exactly one terminal choice (finish_reason stop), content from
// choices[0].message.content as a non-blank string or an array of text parts
// joined with newlines.
func ParseResponse(body []byte) (Response, error) {
	text := strings.TrimPrefix(strings.TrimSpace(string(body)), "\ufeff")
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return Response{}, invalidResponse("malformed JSON payload")
	}
	if err := decoder.Decode(&raw); err != io.EOF {
		return Response{}, invalidResponse("response must contain exactly one JSON object")
	}
	envelope, ok := raw.(map[string]any)
	if !ok {
		return Response{}, invalidResponse("response must contain exactly one JSON object")
	}

	observed := ""
	if model, ok := envelope["model"].(string); ok {
		if trimmed := strings.TrimSpace(model); trimmed != "" {
			observed = trimmed
		}
	}
	usage, err := parseUsage(envelope["usage"])
	if err != nil {
		return Response{}, err
	}
	content, err := extractContent(envelope)
	if err != nil {
		return Response{}, err
	}
	return Response{ObservedModel: observed, Usage: usage, Content: content}, nil
}

func parseUsage(value any) (Usage, error) {
	var usage Usage
	object, ok := value.(map[string]any)
	if !ok {
		return usage, nil
	}
	prompt, err := firstUsageToken(object, "input_tokens", "prompt_tokens")
	if err != nil {
		return Usage{}, err
	}
	completion, err := firstUsageToken(object, "output_tokens", "completion_tokens")
	if err != nil {
		return Usage{}, err
	}
	reasoning, err := reasoningUsageToken(object)
	if err != nil {
		return Usage{}, err
	}
	usage.PromptTokens = int(prompt)
	usage.CompletionTokens = int(completion)
	usage.ReasoningTokens = int(reasoning)
	return usage, nil
}

func firstUsageToken(object map[string]any, names ...string) (int64, error) {
	for _, name := range names {
		raw, present := object[name]
		if !present || raw == nil {
			continue
		}
		return usageToken(raw, name)
	}
	return 0, nil
}

func reasoningUsageToken(object map[string]any) (int64, error) {
	if raw, present := object["reasoning_tokens"]; present && raw != nil {
		return usageToken(raw, "reasoning_tokens")
	}
	if details, ok := object["completion_tokens_details"].(map[string]any); ok {
		if raw, present := details["reasoning_tokens"]; present && raw != nil {
			return usageToken(raw, "completion_tokens_details.reasoning_tokens")
		}
	}
	return 0, nil
}

func usageToken(raw any, name string) (int64, error) {
	number, ok := raw.(json.Number)
	if !ok {
		return 0, invalidResponse(fmt.Sprintf("usage field %s is not a number", name))
	}
	value, err := number.Int64()
	if err != nil {
		return 0, invalidResponse(fmt.Sprintf("usage field %s is not an integer", name))
	}
	return value, nil
}

func extractContent(envelope map[string]any) (string, error) {
	choices, ok := envelope["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", invalidResponse("chat envelope carries no choices")
	}
	if len(choices) > 1 {
		return "", invalidResponse("chat envelope carries ambiguous multiple choices")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return "", invalidResponse("chat envelope carries no choices")
	}
	finishReason, ok := choice["finish_reason"].(string)
	if !ok || finishReason != "stop" {
		return "", invalidResponse("chat envelope is not terminal; finish_reason must be stop")
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return "", invalidResponse("chat choice carries no message content")
	}
	switch content := message["content"].(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return "", invalidResponse("chat choice carries no message content")
		}
		return content, nil
	case []any:
		texts := make([]string, 0, len(content))
		for _, part := range content {
			object, ok := part.(map[string]any)
			if !ok {
				continue
			}
			text, ok := object["text"].(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			texts = append(texts, text)
		}
		if len(texts) == 0 {
			return "", invalidResponse("chat choice carries no message content")
		}
		return strings.Join(texts, "\n"), nil
	default:
		return "", invalidResponse("chat choice carries no message content")
	}
}

func invalidResponse(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidResponse, detail)
}
