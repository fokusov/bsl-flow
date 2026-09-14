// Package specreview ports the assisted single-reviewer OpenCode route of the
// 1c-spec-review skill: Get-BSLFlowReviewPolicy, Assert-BSLFlowReviewPayload
// (raw), Complete-BSLFlowReview (weighted score + overengineering metrics +
// gate verdict) and the OpenCode event/payload extraction, plus the metric
// record (Add-1CSpecRunMetric.ps1). The council route is served by
// cli/internal/councilengine; this package only owns the legacy opencode_compat
// single-reviewer route and its derived artifacts.
package specreview

import (
	"strconv"
	"strings"

	"bsl-flow/cli/internal/councilengine"
)

// Policy is the single-reviewer threshold policy (Get-BSLFlowReviewPolicy).
type Policy struct {
	ReadMode                       string
	PassWeightedScore              float64
	BlockBelowWeightedScore        float64
	MaxOverengineeringIndexForPass int
	MaxUnjustifiedRatioForPass     float64
}

// ParsePolicy ports Get-BSLFlowReviewPolicy.
func ParsePolicy(configText string) (Policy, error) {
	readMode, err := councilengine.YamlValue(configText, []string{"review", "permissions", "project_read_mode"}, "read_search")
	if err != nil {
		return Policy{}, err
	}
	if readMode != "read_search" && readMode != "attached_only" {
		return Policy{}, invalidf("Invalid project_read_mode: %s", readMode)
	}
	for _, forbidden := range []string{"edit", "shell", "subagents", "web", "external_directory"} {
		raw, err := councilengine.YamlValue(configText, []string{"review", "permissions", forbidden}, "false")
		if err != nil {
			return Policy{}, err
		}
		enabled, err := parseBool(raw, "review.permissions."+forbidden)
		if err != nil {
			return Policy{}, err
		}
		if enabled {
			return Policy{}, invalidf("Unsafe reviewer permission cannot be enabled: %s", forbidden)
		}
	}
	passScore, err := yamlFloat(configText, []string{"review", "thresholds", "pass_weighted_score"}, 4.3, "pass_weighted_score")
	if err != nil {
		return Policy{}, err
	}
	blockScore, err := yamlFloat(configText, []string{"review", "thresholds", "block_below_weighted_score"}, 3.5, "block_below_weighted_score")
	if err != nil {
		return Policy{}, err
	}
	maxIndex, err := yamlInt(configText, []string{"review", "thresholds", "max_overengineering_index_for_pass"}, 1, "max_overengineering_index_for_pass")
	if err != nil {
		return Policy{}, err
	}
	maxRatio, err := yamlFloat(configText, []string{"review", "thresholds", "max_unjustified_ratio_for_pass"}, 0, "max_unjustified_ratio_for_pass")
	if err != nil {
		return Policy{}, err
	}
	if passScore < 1 || passScore > 5 || blockScore < 1 || blockScore > 5 {
		return Policy{}, invalidf("Review score thresholds must be between 1 and 5.")
	}
	if blockScore > passScore {
		return Policy{}, invalidf("block_below_weighted_score must not exceed pass_weighted_score.")
	}
	if maxIndex < 0 {
		return Policy{}, invalidf("max_overengineering_index_for_pass must not be negative.")
	}
	if maxRatio < 0 || maxRatio > 1 {
		return Policy{}, invalidf("max_unjustified_ratio_for_pass must be between 0 and 1.")
	}
	return Policy{
		ReadMode:                       readMode,
		PassWeightedScore:              passScore,
		BlockBelowWeightedScore:        blockScore,
		MaxOverengineeringIndexForPass: maxIndex,
		MaxUnjustifiedRatioForPass:     maxRatio,
	}, nil
}

func parseBool(value, name string) (bool, error) {
	switch strings.ToLower(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, invalidf("Expected true or false for %s, got: %s", name, value)
	}
}

func yamlFloat(text string, path []string, defaultValue float64, name string) (float64, error) {
	raw, err := councilengine.YamlValue(text, path, strconv.FormatFloat(defaultValue, 'g', -1, 64))
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, invalidf("Invalid %s: %s", name, raw)
	}
	return parsed, nil
}

func yamlInt(text string, path []string, defaultValue int, name string) (int, error) {
	raw, err := councilengine.YamlValue(text, path, strconv.Itoa(defaultValue))
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, invalidf("Invalid %s: %s", name, raw)
	}
	return parsed, nil
}
