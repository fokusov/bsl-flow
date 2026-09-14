package specreview

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"bsl-flow/cli/internal/councilengine"
)

// metric.go ports Add-1CSpecRunMetric.ps1: the privacy-minimized cross-project
// eval record appended to spec-runs.jsonl after final validation passes.

var changeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// MetricInputs carries the change artifacts for the metric record.
type MetricInputs struct {
	ProjectRoot     string
	ChangeName      string
	AuthorModel     string
	AuthorReasoning string
	MetricsPath     string
}

// MetricResult is the appended record identity.
type MetricResult struct {
	RunID       string
	MetricsPath string
}

// RecordMetric ports Add-1CSpecRunMetric: validate inputs, run the final gate
// freshness check and append one deduplicated JSONL record.
func RecordMetric(inputs MetricInputs, now func() time.Time) (MetricResult, error) {
	projectRoot := strings.TrimRight(inputs.ProjectRoot, `\/`)
	if !changeNamePattern.MatchString(inputs.ChangeName) {
		return MetricResult{}, invalidf("Unsafe OpenSpec change name: %s", inputs.ChangeName)
	}
	changeRoot := filepathJoin(projectRoot, "openspec", "changes", inputs.ChangeName)
	reviewPath := filepathJoin(changeRoot, "review.json")
	reconciliationPath := filepathJoin(changeRoot, "review-reconciliation.json")
	validationPath := filepathJoin(changeRoot, "final-validation.json")
	specPath := filepathJoin(changeRoot, "spec.md")
	for _, path := range []string{reviewPath, reconciliationPath, validationPath, specPath} {
		if _, err := os.Stat(path); err != nil {
			return MetricResult{}, invalidf("Metric input missing: %s", path)
		}
	}
	review, err := readOrdered(reviewPath)
	if err != nil {
		return MetricResult{}, invalidf("Metric input missing: %s", reviewPath)
	}
	reconciliation, err := readOrdered(reconciliationPath)
	if err != nil {
		return MetricResult{}, invalidf("Metric input missing: %s", reconciliationPath)
	}
	validation, err := readOrdered(validationPath)
	if err != nil {
		return MetricResult{}, invalidf("Metric input missing: %s", validationPath)
	}
	if passed, ok := validation.Get("passed").(bool); !ok || !passed {
		return MetricResult{}, invalidf("Metrics are appended only after final validation passes.")
	}
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return MetricResult{}, invalidf("Metric input missing: %s", specPath)
	}
	complexity := regexp.MustCompile(`(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$`).FindStringSubmatch(string(specData))
	risk := regexp.MustCompile(`(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$`).FindStringSubmatch(string(specData))
	if len(complexity) != 2 || len(risk) != 2 {
		return MetricResult{}, invalidf("spec.md must carry exactly one complexity and one risk classification.")
	}
	councilV2 := false
	if version, ok := asInt(review.Get("schema_version")); ok && version == 2 {
		councilV2 = true
	}
	projectID := shortHash([]byte(strings.ToLower(projectRoot)), 16)
	reviewData, _ := os.ReadFile(reviewPath)
	runID := fullHash([]byte(projectID + "|" + inputs.ChangeName + "|" + councilengine.Sha256Hex(reviewData)))
	changeID := shortHash([]byte(projectID+"|"+inputs.ChangeName), 16)

	decisions, _ := reconciliation.Get("decisions").([]any)
	accepted := 0
	rejected := 0
	for _, raw := range decisions {
		decision, _ := raw.(*councilengine.Ordered)
		if d, ok := decision.Get("decision").(string); ok {
			if d == "accepted" {
				accepted++
			} else if d == "rejected" {
				rejected++
			}
		}
	}
	findings, _ := review.Get("findings").([]any)
	total := len(findings)
	acceptanceRate := any(nil)
	if total > 0 {
		acceptanceRate = roundEven(float64(accepted)/float64(total), 4)
	}

	reviewer := any(nil)
	reviewerVerdict := any(nil)
	scores := any(nil)
	weightedScore := any(nil)
	overengineering := any(nil)
	if councilV2 {
		reviewer = "council"
		chair, _ := review.Get("chair").(*councilengine.Ordered)
		if chair != nil {
			reviewerVerdict = chair.Get("verdict")
		}
		overengineering = councilengine.OrderedFrom(
			[]string{"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count", "index", "optional_ratio", "unjustified_ratio", "normalized_index"},
			[]any{nil, nil, nil, nil, nil, nil, nil, nil, nil},
		)
	} else {
		reviewer = review.Get("reviewer")
		reviewerVerdict = review.Get("reviewer_verdict")
		scores = review.Get("scores")
		weightedScore = review.Get("weighted_score")
		rawOE, _ := review.Get("overengineering").(*councilengine.Ordered)
		if rawOE != nil {
			overengineering = councilengine.OrderedFrom(
				[]string{"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count", "index", "optional_ratio", "unjustified_ratio", "normalized_index"},
				[]any{
					rawOE.Get("architectural_decision_count"), rawOE.Get("required_count"), rawOE.Get("justified_count"),
					rawOE.Get("optional_count"), rawOE.Get("unjustified_count"), rawOE.Get("index"),
					rawOE.Get("optional_ratio"), rawOE.Get("unjustified_ratio"), rawOE.Get("normalized_index"),
				},
			)
		}
	}

	record := councilengine.OrderedFrom(
		[]string{"schema_version", "run_id", "recorded_at_utc", "project_id", "change_id", "complexity", "risk", "author", "reviewer", "reviewer_verdict", "gate_verdict", "scores", "weighted_score", "overengineering", "findings", "review_iterations", "human", "implementation", "usage"},
		[]any{
			1, runID, now().UTC().Format("2006-01-02T15:04:05.0000000Z"), projectID, changeID,
			strings.ToUpper(complexity[1]), strings.ToLower(risk[1]),
			councilengine.OrderedFrom([]string{"model", "reasoning"}, []any{nullableStr2(inputs.AuthorModel), nullableStr2(inputs.AuthorReasoning)}),
			reviewer, reviewerVerdict, review.Get("verdict"), scores, weightedScore, overengineering,
			councilengine.OrderedFrom([]string{"total", "accepted", "rejected", "acceptance_rate"}, []any{total, accepted, rejected, acceptanceRate}),
			1,
			councilengine.OrderedFrom([]string{"accepted", "edit_minutes"}, []any{nil, nil}),
			councilengine.OrderedFrom([]string{"passed", "clarifications", "rework_count"}, []any{nil, nil, nil}),
			councilengine.OrderedFrom([]string{"tokens", "duration_sec", "cost"}, []any{nil, nil, nil}),
		},
	)
	line, err := councilengine.ConvertToJSONCompress(record, 20)
	if err != nil {
		return MetricResult{}, invalidf("%v", err)
	}
	metricsPath := inputs.MetricsPath
	if metricsPath == "" {
		home, _ := os.UserHomeDir()
		metricsPath = filepathJoin(home, ".bsl-flow", "evals", "spec-runs.jsonl")
	}
	if err := appendMetricLine(metricsPath, runID, string(line)); err != nil {
		return MetricResult{}, err
	}
	return MetricResult{RunID: runID, MetricsPath: metricsPath}, nil
}

func appendMetricLine(metricsPath, runID, line string) error {
	directory := filepathDir(metricsPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return blockedf("%v", err)
	}
	existing, err := os.ReadFile(metricsPath)
	if err != nil && !os.IsNotExist(err) {
		return blockedf("%v", err)
	}
	for _, existingLine := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(existingLine) == "" {
			continue
		}
		var record struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(existingLine), &record); err != nil {
			return invalidf("Metrics file contains invalid JSONL and was not changed: %s", metricsPath)
		}
		if record.RunID == runID {
			return invalidf("Metric run already recorded: %s", runID)
		}
	}
	file, err := os.OpenFile(metricsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return blockedf("%v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return blockedf("%v", err)
	}
	return nil
}

func readOrdered(path string) (*councilengine.Ordered, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, err := councilengine.DecodeOrdered(data)
	if err != nil {
		return nil, err
	}
	object, ok := value.(*councilengine.Ordered)
	if !ok {
		return nil, invalidf("not an object")
	}
	return object, nil
}

func shortHash(data []byte, length int) string {
	return fullHash(data)[:length]
}

func fullHash(data []byte) string {
	return councilengine.Sha256Hex(data)
}

func nullableStr2(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `\/`)
	if index < 0 {
		return "."
	}
	return path[:index]
}
