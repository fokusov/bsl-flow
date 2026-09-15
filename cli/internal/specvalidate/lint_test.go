package specvalidate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// lintBaselineSpec satisfies every rule of Test-1CSpec.ps1; the negative
// table below mutates exactly one rule at a time.
const lintBaselineSpec = `# change

## Классификация

- Сложность: S
- Риск: low

## Цель

Исправить ошибку в форме документа.

## Требуемое поведение

1. Форма сохраняет документ.
2. Поле пересчитывает итог.

## Контекст 1С

- Объект изменений: форма документа.

## Не делать

- Не менять другие формы.

## Критерии приёмки

- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.

## Требуемые проверки

- [x] Unit — проверка сохранения формы и пересчёта итога.

## Неопределённости / допущения

Допущений нет.
`

func findingLines(findings []Finding) []string {
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%d", finding.Severity, finding.Rule, finding.Message, finding.Line))
	}
	return lines
}

func assertFindings(t *testing.T, spec string, expected []string) {
	t.Helper()
	findings, err := LintSpec([]byte(spec))
	if err != nil {
		t.Fatalf("LintSpec returned error: %v", err)
	}
	actual := findingLines(findings)
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("findings mismatch\nexpected:\n  %s\ngot:\n  %s", strings.Join(expected, "\n  "), strings.Join(actual, "\n  "))
	}
}

// TestLintSpecRealAdoptionSpec mirrors the change's committed spec-lint.json
// artifact (openspec/changes/native-task-activation-adoption/spec-lint.json:
// zero errors, zero warnings).
func TestLintSpecRealAdoptionSpecMatchesArtifact(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "native-task-activation-adoption-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	findings, err := LintSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got: %+v", findings)
	}
}

func TestLintSpecBaselinePasses(t *testing.T) {
	assertFindings(t, lintBaselineSpec, nil)
}

// TestLintSpecAcceptsEnglishHeadings pins the bilingual alternations of
// Test-1CSpec.ps1:45-52.
func TestLintSpecAcceptsEnglishHeadings(t *testing.T) {
	assertFindings(t, strings.Replace(lintBaselineSpec, "## Классификация", "## Classification", 1), nil)
}

func TestLintSpecAcceptanceBulletStructured(t *testing.T) {
	bullet := strings.Replace(lintBaselineSpec,
		"- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.",
		"- Проверить вручную что форма открывается, сохраняется и закрывается без ошибок интерфейса.",
		1)
	assertFindings(t, bullet, nil)
}

// lintNegativeCases is the one-rule-per-case table; the pwsh parity harness
// (crosscheck_test.go) replays the exact same fixtures through the real
// PowerShell validator.
var lintNegativeCases = []struct {
	name     string
	spec     string
	expected []string
}{
	{
		name: "empty spec cascades section and classification errors",
		spec: "\n   \n",
		expected: []string{
			"error|spec.empty|spec.md is empty.|1",
			"error|section.missing|Missing required section: classification.|1",
			"error|section.missing|Missing required section: goal.|1",
			"error|section.missing|Missing required section: required behavior.|1",
			"error|section.missing|Missing required section: 1C context.|1",
			"error|section.missing|Missing required section: non-goals.|1",
			"error|section.missing|Missing required section: acceptance criteria.|1",
			"error|section.missing|Missing required section: verification.|1",
			"error|section.missing|Missing required section: uncertainties.|1",
			"error|classification.complexity|Specification must contain exactly one Complexity value: S, M, or L.|1",
			"error|classification.risk|Specification must contain exactly one Risk value: low, medium, or high.|1",
		},
	},
	{
		name:     "missing goal section",
		spec:     strings.Replace(lintBaselineSpec, "## Цель\n\nИсправить ошибку в форме документа.\n\n", "", 1),
		expected: []string{"error|section.missing|Missing required section: goal.|1"},
	},
	{
		name:     "duplicate goal section",
		spec:     lintBaselineSpec + "## Цель\n\nПовтор раздела.\n",
		expected: []string{"error|section.duplicate|Section appears more than once: goal.|36"},
	},
	{
		name: "classification without values lacks substance",
		spec: strings.Replace(lintBaselineSpec, "- Сложность: S\n- Риск: low\n", "", 1),
		expected: []string{
			"error|section.empty|Section requires substantive continuation: classification.|3",
			"error|classification.complexity|Specification must contain exactly one Complexity value: S, M, or L.|1",
			"error|classification.risk|Specification must contain exactly one Risk value: low, medium, or high.|1",
		},
	},
	{
		name:     "complexity value missing",
		spec:     strings.Replace(lintBaselineSpec, "- Сложность: S\n", "", 1),
		expected: []string{"error|classification.complexity|Specification must contain exactly one Complexity value: S, M, or L.|1"},
	},
	{
		name:     "risk value outside vocabulary",
		spec:     strings.Replace(lintBaselineSpec, "- Риск: low", "- Риск: critical", 1),
		expected: []string{"error|classification.risk|Specification must contain exactly one Risk value: low, medium, or high.|1"},
	},
	{
		name:     "TODO placeholder",
		spec:     strings.Replace(lintBaselineSpec, "Исправить ошибку в форме документа.", "Исправить ошибку в форме документа. TODO", 1),
		expected: []string{"error|placeholder.template|Unresolved template placeholder: TODO|10"},
	},
	{
		name:     "angle bracket placeholder",
		spec:     strings.Replace(lintBaselineSpec, "Исправить ошибку в форме документа.", "Выбрать сложность: <S|M|L>.", 1),
		expected: []string{"error|placeholder.template|Unresolved template placeholder: <S|M|L>|10"},
	},
	{
		name: "acceptance too short also lacks structure",
		spec: strings.Replace(lintBaselineSpec,
			"- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.",
			"Мало.", 1),
		expected: []string{
			"error|acceptance.too-short|Acceptance criteria are empty or too short.|25",
			"error|acceptance.not-structured|Acceptance criteria are not objectively structured.|25",
		},
	},
	{
		name:     "acceptance without keywords or bullets",
		spec:     strings.Replace(lintBaselineSpec, "- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.", "Просто длинный текст критериев без структуры и сценария.", 1),
		expected: []string{"error|acceptance.not-structured|Acceptance criteria are not objectively structured.|25"},
	},
	{
		name:     "scenario missing THEN points at the scenario line",
		spec:     strings.Replace(lintBaselineSpec, "- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.", "- GIVEN заполненная форма WHEN пользователь сохраняет документ", 1),
		expected: []string{"error|acceptance.scenario|Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.|27"},
	},
	{
		name: "each broken scenario points at its own GIVEN line",
		spec: strings.Replace(lintBaselineSpec,
			"- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.",
			"- GIVEN заполненная форма WHEN пользователь сохраняет документ\n- GIVEN записанный документ THEN форма закрывается", 1),
		expected: []string{
			"error|acceptance.scenario|Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.|27",
			"error|acceptance.scenario|Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.|28",
		},
	},
	{
		// Two broken GIVEN scenarios sharing one bullet collapse into a
		// single finding: the formatted "spec.md line N: message" pair is
		// identical (Test-1CSpec.ps1 Add-SpecError deduplicates too).
		name: "two broken scenarios in one bullet deduplicate",
		spec: strings.Replace(lintBaselineSpec,
			"- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.",
			"- GIVEN заполненная форма WHEN пользователь сохраняет GIVEN документ записан THEN форма закрыта", 1),
		expected: []string{
			"error|acceptance.scenario|Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.|27",
		},
	},
	{
		// Both the PowerShell validator and the Go port point the
		// level-detail finding at the item's own line; the shrunken body
		// also trips the 20-character checklist floor (section-level,
		// Test-1CSpec.ps1:108-111).
		name: "verification level without detail",
		spec: strings.Replace(lintBaselineSpec, "- [x] Unit — проверка сохранения формы и пересчёта итога.", "- [x] Unit", 1),
		expected: []string{
			"error|verification.level-detail|Each selected verification level must describe what it proves.|31",
			"error|verification.empty|Required verification must describe a concrete check or an explicit evidence blocker, not an empty checklist.|29",
		},
	},
	{
		name:     "verification only unchecked items",
		spec:     strings.Replace(lintBaselineSpec, "- [x] Unit — проверка сохранения формы и пересчёта итога.", "- [ ] Unit", 1),
		expected: []string{"error|verification.empty|Required verification must describe a concrete check or an explicit evidence blocker, not an empty checklist.|29"},
	},
	{
		name:     "speculative language warning",
		spec:     strings.Replace(lintBaselineSpec, "Исправить ошибку в форме документа.", "Исправить ошибку в форме документа. Оставим это на будущее.", 1),
		expected: []string{"warning|spec.speculative|Potential speculative design language found; verify concrete justification.|0"},
	},
}

func TestLintSpecNegativeTable(t *testing.T) {
	for _, tt := range lintNegativeCases {
		t.Run(tt.name, func(t *testing.T) {
			assertFindings(t, tt.spec, tt.expected)
		})
	}
}

func TestLintSpecLengthWarning(t *testing.T) {
	padded := strings.Replace(lintBaselineSpec,
		"Исправить ошибку в форме документа.",
		"Исправить ошибку. "+strings.Repeat("подробности ", 3000), 1)
	length := utf8.RuneCountInString(padded) // every rune is BMP, so runes == UTF-16 units
	if length <= maxSpecCharacters {
		t.Fatalf("fixture must exceed %d characters, has %d", maxSpecCharacters, length)
	}
	assertFindings(t, padded, []string{
		fmt.Sprintf("warning|spec.length|Specification length %d exceeds 30000 characters; verify that detail is necessary.|0", length),
	})
}

func TestLintSpecDeterministicAcrossRuns(t *testing.T) {
	first, err := LintSpec([]byte(lintBaselineSpec + "TODO\n"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := LintSpec([]byte(lintBaselineSpec + "TODO\n"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(findingLines(first), "\n") != strings.Join(findingLines(again), "\n") {
			t.Fatalf("lint is not deterministic: %+v vs %+v", first, again)
		}
	}
}

func TestLintSpecRejectsInvalidUTF8(t *testing.T) {
	findings, err := LintSpec([]byte{0x23, 0x20, 0xff, 0xfe, 0x0a})
	if err == nil {
		t.Fatalf("expected an error for invalid UTF-8, got findings %+v", findings)
	}
}
