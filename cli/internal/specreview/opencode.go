package specreview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"bsl-flow/cli/internal/councilengine"
)

// opencode.go ports the single-reviewer OpenCode provider route of
// Invoke-1CSpecReview.ps1: the sealed context envelope, the bounded
// `opencode run --pure` spawn, the events.jsonl stream parsing
// (Get-BSLFlowJsonFromOpenCodeEvents) and the strict single-object extraction
// (Get-BSLFlowReviewPayloadFromOpenCodeText).

// ReviewerFiles locates the packaged reviewer assets under the skill root.
type ReviewerFiles struct {
	SkillRoot      string
	ReviewerConfig string // opencode-reviewer.json
	RubricPath     string // references/reviewer-rubric.md
	PromptPath     string // reviewer/spec-reviewer-prompt.md
}

func resolveReviewerFiles(skillRoot string) (ReviewerFiles, error) {
	files := ReviewerFiles{
		SkillRoot:      skillRoot,
		ReviewerConfig: skillRoot + string(os.PathSeparator) + "reviewer" + string(os.PathSeparator) + "opencode-reviewer.json",
		RubricPath:     skillRoot + string(os.PathSeparator) + "references" + string(os.PathSeparator) + "reviewer-rubric.md",
		PromptPath:     skillRoot + string(os.PathSeparator) + "reviewer" + string(os.PathSeparator) + "spec-reviewer-prompt.md",
	}
	for _, path := range []string{files.ReviewerConfig, files.RubricPath, files.PromptPath} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return files, blockedf("Packaged reviewer file missing: %s", path)
		}
	}
	return files, nil
}

// ResolveReviewerFiles locates the packaged reviewer assets under a skill root.
func ResolveReviewerFiles(skillRoot string) (ReviewerFiles, error) {
	return resolveReviewerFiles(skillRoot)
}

// ReviewerRequest is the single-reviewer dispatch input.
type ReviewerRequest struct {
	ProjectRoot     string
	Agent           string
	Model           string
	Variant         string
	OpenCodePath    string
	TimeoutSeconds  int
	MaxOutputBytes  int64
	ContextEnvelope string
}

// ReviewerResult is the raw parsed review payload plus the retained run
// evidence paths.
type ReviewerResult struct {
	RawReview  *councilengine.Ordered
	RunRoot    string
	StatusPath string
}

// RunReviewer ports the single-reviewer provider spawn + stream drain.
func RunReviewer(ctx context.Context, req ReviewerRequest, files ReviewerFiles, now func() time.Time) (*councilengine.Ordered, error) {
	providerPath := req.OpenCodePath
	if strings.TrimSpace(providerPath) == "" {
		resolved, err := exec.LookPath("opencode")
		if err != nil {
			return nil, blockedf("OpenCode CLI is required for this review route.")
		}
		providerPath = resolved
	}
	if _, err := os.Stat(providerPath); err != nil {
		return nil, blockedf("OpenCode executable not found: %s", providerPath)
	}
	runID := now().UTC().Format("20060102T150405") + "-" + strings.ToLower(newGuidN())
	runRoot := filepathJoin(req.ProjectRoot, ".bsl-flow", "reports", "spec-review", runID)
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	eventsPath := filepathJoin(runRoot, "events.jsonl")
	rawPath := filepathJoin(runRoot, "raw-response.txt")
	stderrPath := filepathJoin(runRoot, "provider-stderr.log")

	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	arguments := []string{"run", "--pure", "--agent", req.Agent, "--model", req.Model, "--variant", req.Variant, "--format", "json", "--dir", req.ProjectRoot, "Review the delimited specification context from stdin. Return only the contracted JSON object."}
	cmd := exec.CommandContext(commandCtx, providerPath, arguments...)
	cmd.Dir = req.ProjectRoot
	cmd.Env = append(os.Environ(),
		"OPENCODE_CONFIG="+files.ReviewerConfig,
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
	)
	cmd.Stdin = strings.NewReader(req.ContextEnvelope)
	output, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(err.Error(), "exit status") {
		if commandCtx.Err() == context.DeadlineExceeded {
			return nil, blockedf("OpenCode review exceeded timeout of %d seconds.", req.TimeoutSeconds)
		}
		return nil, blockedf("OpenCode review failed: %v", err)
	}
	if int64(len(output)) > req.MaxOutputBytes {
		return nil, blockedf("OpenCode output exceeded %d bytes.", req.MaxOutputBytes)
	}
	// Retain the raw stream without copying it into review.json or metrics.
	if err := os.WriteFile(rawPath, output, 0o644); err != nil {
		return nil, blockedf("%v", err)
	}
	_ = eventsPath
	_ = stderrPath
	lines := splitLines(string(output))
	events := collectOpenCodeEvents(lines)
	if err := writeJSONLEvents(eventsPath, events); err != nil {
		return nil, blockedf("%v", err)
	}
	rawReview, err := ReviewFromOpenCodeEvents(lines)
	if err != nil {
		return nil, err
	}
	return rawReview, nil
}

func filepathJoin(elements ...string) string {
	return filepath.Join(elements...)
}

func newGuidN() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return hex.EncodeToString(bytes[:])
}

// ReviewFromOpenCodeEvents ports Get-BSLFlowJsonFromOpenCodeEvents.
func ReviewFromOpenCodeEvents(lines []string) (*councilengine.Ordered, error) {
	parts := []string{}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event struct {
			Type string          `json:"type"`
			Part json.RawMessage `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "text" && len(event.Part) > 0 {
			var part struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(event.Part, &part); err == nil && part.Text != "" {
				parts = append(parts, part.Text)
			}
		} else if event.Type == "error" {
			return nil, blockedf("OpenCode returned an error event: %s", line)
		}
	}
	if len(parts) == 0 {
		return nil, blockedf("OpenCode returned no completed text event.")
	}
	var joinErrors []string
	for _, joined := range []string{strings.Join(parts, ""), strings.Join(parts, "\n")} {
		text := strings.TrimSpace(joined)
		if text == "" {
			continue
		}
		payload, err := ReviewPayloadFromOpenCodeText(text)
		if err == nil {
			return payload, nil
		}
		joinErrors = append(joinErrors, err.Error())
	}
	if len(joinErrors) == 0 {
		return nil, blockedf("OpenCode returned no completed text event.")
	}
	return nil, invalidf("OpenCode text was not one JSON object: %s", joinErrors[0])
}

// ReviewPayloadFromOpenCodeText ports Get-BSLFlowReviewPayloadFromOpenCodeText.
func ReviewPayloadFromOpenCodeText(text string) (*councilengine.Ordered, error) {
	trimmed := strings.TrimSpace(text)
	fencePattern := regexp.MustCompile(`(?ims)^` + "```" + `(?:json)?[ \t]*\r?\n(.*?)^` + "```" + `[ \t]*\r?$`)
	blocks := fencePattern.FindAllStringSubmatch(trimmed, -1)
	if len(blocks) > 0 {
		if len(blocks) != 1 {
			return nil, invalidf("OpenCode returned multiple fenced objects; review is ambiguous.")
		}
		outside := strings.Replace(trimmed, blocks[0][0], "", 1)
		if strings.Contains(outside, "```") {
			return nil, invalidf("OpenCode returned additional fenced text outside the review block.")
		}
		if hasStructuredCandidate(outside) {
			return nil, invalidf("OpenCode returned additional structured text outside the review block.")
		}
		trimmed = strings.TrimSpace(blocks[0][1])
	}
	if strings.HasPrefix(trimmed, "[") {
		return nil, invalidf("Review must be a JSON object.")
	}
	value, err := councilengine.DecodeOrdered([]byte(trimmed))
	if err != nil {
		return nil, invalidf("OpenCode text was not one JSON object: %v", err)
	}
	object, ok := value.(*councilengine.Ordered)
	if !ok {
		return nil, invalidf("Review must be a JSON object.")
	}
	return object, nil
}

// hasStructuredCandidate reports whether the outside text carries a balanced,
// valid JSON object/array candidate beside the fenced review. Prose such as
// "Result [PASS]" is not a candidate; "{"extra":1}" is.
func hasStructuredCandidate(outside string) bool {
	stack := []byte{}
	startStack := []int{}
	inString := false
	escaped := false
	for cursor := 0; cursor < len(outside); cursor++ {
		character := outside[cursor]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		if len(stack) > 0 && character == '"' {
			inString = true
			continue
		}
		if character == '{' || character == '[' {
			stack = append(stack, character)
			startStack = append(startStack, cursor)
			continue
		}
		if character != '}' && character != ']' {
			continue
		}
		if len(stack) == 0 {
			continue
		}
		opening := stack[len(stack)-1]
		if (opening == '{' && character != '}') || (opening == '[' && character != ']') {
			return true
		}
		start := startStack[len(startStack)-1]
		stack = stack[:len(stack)-1]
		startStack = startStack[:len(startStack)-1]
		if len(stack) == 0 {
			if isValidJSONContainer(outside[start : cursor+1]) {
				return true
			}
		}
	}
	return len(stack) != 0
}

func isValidJSONContainer(text string) bool {
	value, err := councilengine.DecodeOrdered([]byte(text))
	if err != nil {
		return false
	}
	switch value.(type) {
	case *councilengine.Ordered, []any:
		return true
	}
	return false
}

func collectOpenCodeEvents(lines []string) []map[string]any {
	events := []map[string]any{}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

func writeJSONLEvents(path string, events []map[string]any) error {
	var builder strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func splitLines(text string) []string {
	return regexp.MustCompile(`\r?\n`).Split(text, -1)
}
