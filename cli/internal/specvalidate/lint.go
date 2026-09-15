// Package specvalidate ports the deterministic 1C OpenSpec specification
// rules from the PowerShell workflow (global/skills/1c-spec-review/scripts)
// so the native Go controller can lint a spec.md and validate a change
// directory's final review evidence without invoking pwsh.
//
// Contract: every rule is a direct port of a PowerShell check and each
// helper cites the script and line it mirrors.  All functions are pure:
// identical input bytes always produce identical findings, with no clocks,
// locales or random state.  Where PowerShell matched case-insensitively,
// explicit ASCII folding (for ASCII keywords) or Unicode simple case folding
// (for Cyrillic phrases) is applied so results never depend on the
// machine's culture.
package specvalidate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Finding is one lint result.  Severity mirrors the two buckets the
// PowerShell lint emits (Test-1CSpec.ps1:117-124): "error" entries land in
// the artifact's errors array and flip passed to false, while "warning"
// entries land in warnings and do not.  Message carries the exact
// PowerShell message text and Line the position the PowerShell validator
// reports through its "spec.md line N:" prefix (0 when the PowerShell rule
// reported no line, which is the case for both warnings).
type Finding struct {
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	Line     int    `json:"line"`
}

// Rule identifiers for Findings.  PowerShell reports plain message strings;
// these stable ids are the Go-side rule names used by tests and callers.
const (
	ruleSpecEmpty           = "spec.empty"
	ruleSectionMissing      = "section.missing"
	ruleSectionDuplicate    = "section.duplicate"
	ruleSectionEmpty        = "section.empty"
	ruleComplexity          = "classification.complexity"
	ruleRisk                = "classification.risk"
	rulePlaceholder         = "placeholder.template"
	ruleAcceptanceShort     = "acceptance.too-short"
	ruleAcceptanceStructure = "acceptance.not-structured"
	ruleAcceptanceScenario  = "acceptance.scenario"
	ruleVerificationDetail  = "verification.level-detail"
	ruleVerificationEmpty   = "verification.empty"
	ruleSpecLength          = "spec.length"
	ruleSpeculativeLanguage = "spec.speculative"
	maxSpecCharacters       = 30000 // Test-1CSpec.ps1:6 default MaxCharacters
)

// lintRule references, by severity:
//   - errors   : Test-1CSpec.ps1:43 (empty), 44-61 (sections), 63-66
//     (classification), 68-78 (placeholders), 80-96 (acceptance criteria),
//     98-112 (verification)
//   - warnings : Test-1CSpec.ps1:114-115 (length, speculative language)

// requiredSections mirrors Test-1CSpec.ps1:44-53.  Name is the English
// handle embedded in the PowerShell messages; pattern is the heading body
// alternation after the PowerShell "(?im)^##\s+" prefix.
var requiredSections = []struct {
	Name    string
	Pattern string
}{
	{"classification", `(Классификация|Classification)`},
	{"goal", `(Цель|Goal)`},
	{"required behavior", `(Требуемое поведение|Required behavior)`},
	{"1C context", `(Контекст 1С|1C context)`},
	{"non-goals", `(Не делать|Non-goals)`},
	{"acceptance criteria", `(Критерии при[её]мки|Acceptance criteria)`},
	{"verification", `(Требуемые проверки|Required verification)`},
	{"uncertainties", `(Неопредел[её]нности\s*/\s*допущения|Uncertainties\s*/\s*assumptions)`},
}

// LintSpec validates specification bytes against every rule of
// Test-1CSpec.ps1 and returns the findings in the order the PowerShell
// validator appends them.  It never returns findings together with an
// error; an error means the input itself is unusable.
func LintSpec(spec []byte) ([]Finding, error) {
	if !utf8.Valid(spec) {
		// PowerShell reads spec.md with a lossy UTF-8 decoder and would lint
		// replacement characters; the native path treats invalid UTF-8 as an
		// input error instead of silently linting mojibake.
		return nil, errors.New("spec.md must be valid UTF-8")
	}
	// Test-1CSpec.ps1:17 reads through .NET which strips a UTF-8 BOM.
	text := strings.TrimPrefix(string(spec), "\uFEFF")
	state := &lintState{text: text}
	state.run()
	return state.findings, nil
}

type lintState struct {
	text     string
	findings []Finding
}

func (s *lintState) run() {
	// Test-1CSpec.ps1:43.
	if strings.TrimSpace(s.text) == "" {
		s.addError(ruleSpecEmpty, "spec.md is empty.", 0)
	}
	// Test-1CSpec.ps1:44-61.
	for _, section := range requiredSections {
		heading := regexp.MustCompile(`(?im)^##\s+` + section.Pattern + `\s*$`)
		matches := heading.FindAllStringIndex(s.text, -1)
		if len(matches) == 0 {
			s.addError(ruleSectionMissing, "Missing required section: "+section.Name+".", 0)
			continue
		}
		if len(matches) > 1 {
			s.addError(ruleSectionDuplicate, "Section appears more than once: "+section.Name+".", matches[1][0])
		}
		if span := s.findSection(section.Pattern); span.ok && s.substantiveLineCount(span.body) == 0 {
			s.addError(ruleSectionEmpty, "Section requires substantive continuation: "+section.Name+".", matches[0][0])
		}
	}
	// Test-1CSpec.ps1:63-66.
	if len(regexp.MustCompile(`(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$`).FindAllStringIndex(s.text, -1)) != 1 {
		s.addError(ruleComplexity, "Specification must contain exactly one Complexity value: S, M, or L.", 0)
	}
	if len(regexp.MustCompile(`(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$`).FindAllStringIndex(s.text, -1)) != 1 {
		s.addError(ruleRisk, "Specification must contain exactly one Risk value: low, medium, or high.", 0)
	}
	// Test-1CSpec.ps1:68-78.
	for _, pattern := range placeholderPatterns {
		if match := pattern.FindStringIndex(s.text); match != nil {
			s.addError(rulePlaceholder, "Unresolved template placeholder: "+strings.TrimSpace(s.text[match[0]:match[1]]), match[0])
		}
	}
	if start, matched, found := s.firstAnglePlaceholder(); found {
		s.addError(rulePlaceholder, "Unresolved template placeholder: "+strings.TrimSpace(matched), start)
	}
	// Test-1CSpec.ps1:80-96.
	s.lintAcceptance()
	// Test-1CSpec.ps1:98-112.
	s.lintVerification()
	// Test-1CSpec.ps1:114-115.
	s.lintWarnings()
}

func (s *lintState) addError(rule, message string, offset int) {
	finding := Finding{
		Severity: "error",
		Rule:     rule,
		Message:  message,
		Line:     s.lineAtOffset(offset),
	}
	for _, previous := range s.findings {
		// PowerShell suppresses a repeated identical "spec.md line N:
		// message" pair (Add-SpecError checks $errors.Contains), so two
		// broken scenarios sharing one bullet report once.
		if previous.Severity == "error" && previous.Line == finding.Line && previous.Message == finding.Message {
			return
		}
	}
	s.findings = append(s.findings, finding)
}

func (s *lintState) addWarning(rule, message string) {
	// PowerShell warnings carry no "spec.md line N:" prefix (Test-1CSpec.ps1:114-115).
	s.findings = append(s.findings, Finding{Severity: "warning", Rule: rule, Message: message, Line: 0})
}

// lineAtOffset mirrors Get-SpecLineNumber (Test-1CSpec.ps1:21-24): the line
// holding the given byte offset, 1-based.
func (s *lintState) lineAtOffset(offset int) int {
	if offset < 0 {
		return 1
	}
	if offset > len(s.text) {
		offset = len(s.text)
	}
	return 1 + strings.Count(s.text[:offset], "\n")
}

// sectionSpan is the result of Get-SectionMatch (Test-1CSpec.ps1:28-30).
// bodyStart is the absolute byte offset of body inside the text, so findings
// can point inside the section body rather than at its heading.
type sectionSpan struct {
	start     int
	bodyStart int
	body      string
	ok        bool
}

var nextSectionHeading = regexp.MustCompile(`(?m)^##\s`)

// findSection ports Get-SectionMatch for "(?ims)^##\s+PAT\s*$\s*(?<body>.*?)(?=^##\s|\z)".
// RE2 has no lookahead, so the lazy body is reconstructed as the text up to
// the first later "^##\s" line start (the only positions the PowerShell
// lookahead accepts) or to the end of the text.
func (s *lintState) findSection(pattern string) sectionSpan {
	heading := regexp.MustCompile(`(?im)^##\s+` + pattern + `\s*$`)
	match := heading.FindStringIndex(s.text)
	if match == nil {
		return sectionSpan{}
	}
	bodyStart := match[1]
	for bodyStart < len(s.text) {
		r, size := utf8.DecodeRuneInString(s.text[bodyStart:])
		if !isNetSpace(r) {
			break
		}
		bodyStart += size
	}
	bodyEnd := len(s.text)
	if next := nextSectionHeading.FindStringIndex(s.text[bodyStart:]); next != nil {
		bodyEnd = bodyStart + next[0]
	}
	return sectionSpan{start: match[0], bodyStart: bodyStart, body: s.text[bodyStart:bodyEnd], ok: true}
}

var lintLineSplit = regexp.MustCompile(`\r?\n`)
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
var bulletLabelOnly = regexp.MustCompile(`^[-*]\s*(?:[^:]+):\s*$`)
var loneListItemNumber = regexp.MustCompile(`^\d+[.)]?\s*$`)

// substantiveLineCount ports Get-SubstantiveBody (Test-1CSpec.ps1:31-41):
// non-blank lines that remain after stripping HTML comments, bare
// "Label:" bullets and bare "1." list markers.
func (s *lintState) substantiveLineCount(body string) int {
	cleaned := htmlComment.ReplaceAllString(body, "")
	count := 0
	for _, line := range lintLineSplit.Split(cleaned, -1) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if bulletLabelOnly.MatchString(trimmed) {
			continue
		}
		if loneListItemNumber.MatchString(trimmed) {
			continue
		}
		count++
	}
	return count
}

// placeholderPatterns are the line-anchored template placeholders from
// Test-1CSpec.ps1:68-73 (the first four patterns).
var placeholderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^Кратко:\s*какой результат`),
	regexp.MustCompile(`(?im)^\s*[123]\.\s*$`),
	regexp.MustCompile(`(?im)^\s*-\s*GIVEN\s+\.\.\.`),
	regexp.MustCompile(`(?im)^\s*-\s*(?:Конфигурация/подсистема|Затрагиваемые объекты|Клиент/сервер|Существенные ограничения):\s*$`),
}

// firstAnglePlaceholder ports the fifth placeholder pattern of
// Test-1CSpec.ps1:74: "(?im)<S\|M\|L>|<low\|medium\|high>|\b(TODO|TBD|FIXME)\b".
// Word boundaries and case folding are implemented explicitly so the check
// is culture-independent while matching .NET's Unicode-aware \b.
func (s *lintState) firstAnglePlaceholder() (start int, matched string, found bool) {
	best := -1
	bestText := ""
	for _, literal := range []string{"<s|m|l>", "<low|medium|high>"} {
		if at := indexFoldASCII(s.text, literal); at >= 0 && (best == -1 || at < best) {
			best, bestText = at, s.text[at:at+len(literal)]
		}
	}
	for _, keyword := range []string{"TODO", "TBD", "FIXME"} {
		occurrences := keywordByteOccurrences(s.text, keyword)
		if len(occurrences) > 0 && (best == -1 || occurrences[0] < best) {
			best, bestText = occurrences[0], keyword
		}
	}
	if best == -1 {
		return 0, "", false
	}
	return best, bestText, true
}

func (s *lintState) lintAcceptance() {
	// Test-1CSpec.ps1:80-96.
	span := s.findSection(`(Критерии при[её]мки|Acceptance criteria)`)
	if !span.ok {
		return
	}
	leading := len(span.body) - len(strings.TrimLeftFunc(span.body, isNetSpace))
	bodyStart := span.bodyStart + leading
	body := strings.TrimSpace(span.body)
	if utf16Len(body) < 30 {
		s.addError(ruleAcceptanceShort, "Acceptance criteria are empty or too short.", span.start)
	}
	// (?i)\b(GIVEN|WHEN|THEN)\b over the body (Test-1CSpec.ps1:84).
	runes := []rune(body)
	hasKeyword := len(keywordRuneOccurrences(runes, "GIVEN")) > 0 ||
		len(keywordRuneOccurrences(runes, "WHEN")) > 0 ||
		len(keywordRuneOccurrences(runes, "THEN")) > 0
	if !hasKeyword && !acceptanceBulletStructured(body) {
		s.addError(ruleAcceptanceStructure, "Acceptance criteria are not objectively structured.", span.start)
	}
	given := keywordRuneOccurrences(runes, "GIVEN")
	if len(given) > 0 {
		// Regex.Split on "(?i)\bGIVEN\b" then every part after the first
		// must satisfy "(?is)\S.+?\bWHEN\b\s+\S.+?\bTHEN\b\s+\S"
		// (Test-1CSpec.ps1:87-95).  The finding points at the broken
		// scenario's own GIVEN occurrence instead of the section heading.
		offsets := runeByteOffsets(body)
		for index, at := range given {
			end := len(runes)
			if index+1 < len(given) {
				end = given[index+1]
			}
			if !scenarioHasWhenThen(runes[at+len([]rune("GIVEN")) : end]) {
				s.addError(ruleAcceptanceScenario, "Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.", bodyStart+offsets[at])
			}
		}
	}
}

// acceptanceBulletStructured ports "(?m)^\s*[-*]\s+\S.{15,}$"
// (Test-1CSpec.ps1:84); lengths are counted in UTF-16 code units because
// the PowerShell quantifier counts .NET string characters.
func acceptanceBulletStructured(body string) bool {
	for _, line := range lintLineSplit.Split(body, -1) {
		runes := []rune(line)
		index := 0
		for index < len(runes) && isNetSpace(runes[index]) {
			index++
		}
		if index >= len(runes) || (runes[index] != '-' && runes[index] != '*') {
			continue
		}
		index++
		whitespace := index
		for index < len(runes) && isNetSpace(runes[index]) {
			index++
		}
		if index == whitespace || index >= len(runes) || isNetSpace(runes[index]) {
			continue
		}
		if utf16LenRunes(runes[index:]) >= 16 {
			return true
		}
	}
	return false
}

// scenarioHasWhenThen ports "(?is)\S.+?\bWHEN\b\s+\S.+?\bTHEN\b\s+\S"
// (Test-1CSpec.ps1:91) without regular expressions so the WHEN/THEN word
// boundaries use .NET's Unicode word-character class instead of RE2's
// ASCII-only \b.
func scenarioHasWhenThen(part []rune) bool {
	whens := keywordRuneOccurrences(part, "WHEN")
	thens := keywordRuneOccurrences(part, "THEN")
	for _, when := range whens {
		// \S.+? before WHEN: any non-space rune at least two runes ahead.
		before := false
		for i := 0; i+2 <= when; i++ {
			if !isNetSpace(part[i]) {
				before = true
				break
			}
		}
		if !before {
			continue
		}
		// \s+\S after WHEN.
		first, ok := nonSpaceAfterWhitespace(part, when+4)
		if !ok {
			continue
		}
		for _, then := range thens {
			if then < first+2 {
				continue
			}
			if _, ok := nonSpaceAfterWhitespace(part, then+4); ok {
				return true
			}
		}
	}
	return false
}

// nonSpaceAfterWhitespace reports the index of the first non-space rune
// strictly after from, requiring at least one whitespace rune at from
// (the "\s+\S" tail of the PowerShell scenario pattern).
func nonSpaceAfterWhitespace(runes []rune, from int) (int, bool) {
	if from >= len(runes) || !isNetSpace(runes[from]) {
		return 0, false
	}
	for i := from + 1; i < len(runes); i++ {
		if !isNetSpace(runes[i]) {
			return i, true
		}
	}
	return 0, false
}

var checkedVerificationItem = regexp.MustCompile(`(?im)^\s*[-*]\s+\[x\]\s*(.*)$`)
var levelOnlyDetail = regexp.MustCompile(`^(?i:Static|Unit|Integration|UI|Smoke|Independent review)[\s:—-]*$`)
var uncheckedVerificationLine = regexp.MustCompile(`(?m)^\s*[-*]\s+\[ \].*(?:\r?\n|$)`)

// blankComment blanks one HTML comment byte-for-byte (newlines kept), so the
// surrounding text keeps every offset while the comment can no longer match.
func blankComment(comment string) string {
	blanked := []byte(comment)
	for index, byteValue := range blanked {
		if byteValue != '\r' && byteValue != '\n' {
			blanked[index] = ' '
		}
	}
	return string(blanked)
}

func (s *lintState) lintVerification() {
	// Test-1CSpec.ps1:98-112.
	span := s.findSection(`(Требуемые проверки|Required verification)`)
	if !span.ok {
		return
	}
	// Comment stripping preserves offsets by spacing comments out, so a
	// finding lands on the selected item's own line instead of the section
	// heading (Test-1CSpec.ps1:105).
	raw := htmlComment.ReplaceAllStringFunc(span.body, blankComment)
	leading := len(span.body) - len(strings.TrimLeftFunc(raw, isNetSpace))
	bodyStart := span.bodyStart + leading
	body := strings.TrimSpace(raw)
	for _, check := range checkedVerificationItem.FindAllStringSubmatchIndex(body, -1) {
		detail := strings.TrimSpace(body[check[2]:check[3]])
		if levelOnlyDetail.MatchString(detail) || utf16Len(detail) < 12 {
			s.addError(ruleVerificationDetail, "Each selected verification level must describe what it proves.", bodyStart+check[0])
		}
	}
	withoutUnchecked := strings.TrimSpace(uncheckedVerificationLine.ReplaceAllString(body, ""))
	if utf16Len(withoutUnchecked) < 20 {
		s.addError(ruleVerificationEmpty, "Required verification must describe a concrete check or an explicit evidence blocker, not an empty checklist.", span.start)
	}
}

var speculativeFutureProof = regexp.MustCompile(`future[- ]proof`)
var speculativeUniversal = regexp.MustCompile(`универсальн(?:ый|ая|ое)\s+механизм`)

func (s *lintState) lintWarnings() {
	// Test-1CSpec.ps1:114.
	if length := utf16Len(s.text); length > maxSpecCharacters {
		s.addWarning(ruleSpecLength, fmt.Sprintf("Specification length %d exceeds %d characters; verify that detail is necessary.", length, maxSpecCharacters))
	}
	// Test-1CSpec.ps1:115.  .NET's (?i) folds Cyrillic; Unicode simple case
	// folding via strings.ToLower is the deterministic culture-independent
	// equivalent for these phrases.
	folded := strings.ToLower(s.text)
	if strings.Contains(folded, "на будущее") || speculativeFutureProof.MatchString(folded) || speculativeUniversal.MatchString(folded) {
		s.addWarning(ruleSpeculativeLanguage, "Potential speculative design language found; verify concrete justification.")
	}
}

// runeByteOffsets maps each rune index of text to its byte offset.
func runeByteOffsets(text string) []int {
	offsets := make([]int, 0, utf8.RuneCountInString(text))
	for index := range text {
		offsets = append(offsets, index)
	}
	return offsets
}

// isNetSpace reports whether r is whitespace under the .NET \s class
// (Char.IsWhiteSpace), which Go's ASCII-only regexp \s narrows.
func isNetSpace(r rune) bool {
	return unicode.IsSpace(r)
}

// isNetWord reports whether r is a word character under the .NET \w class
// ([\p{L}\p{Mn}\p{Nd}\p{Pc}]), which Go's ASCII-only \b narrows.
func isNetWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.In(r, unicode.Nd, unicode.Pc)
}

// utf16Len counts UTF-16 code units the way .NET string.Length does, so
// PowerShell length thresholds behave identically for any input.
func utf16Len(text string) int {
	return utf16LenRunes([]rune(text))
}

func utf16LenRunes(runes []rune) int {
	length := 0
	for _, r := range runes {
		if r > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// indexFoldASCII finds the first occurrence of the lowercase ASCII needle
// in text ignoring ASCII case only.
func indexFoldASCII(text, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(text); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if foldASCIIByte(text[i+j]) != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func foldASCIIByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// keywordByteOccurrences returns the byte offsets of keyword (uppercase
// ASCII) in text, ASCII case-insensitively, bounded by .NET-style Unicode
// word boundaries on both sides.
func keywordByteOccurrences(text, keyword string) []int {
	var occurrences []int
	runes := []rune(text)
	offsets := runeByteOffsets(text)
	for _, index := range keywordRuneOccurrences(runes, keyword) {
		occurrences = append(occurrences, offsets[index])
	}
	return occurrences
}

// keywordRuneOccurrences returns the rune indexes of keyword (uppercase
// ASCII) in runes, ASCII case-insensitively, with .NET-style Unicode word
// boundaries around the match (the "\bKEYWORD\b" constructs of
// Test-1CSpec.ps1:74, 84, 87, 91).
func keywordRuneOccurrences(runes []rune, keyword string) []int {
	needle := []rune(keyword)
	var occurrences []int
	for i := 0; i+len(needle) <= len(runes); i++ {
		if !foldRuneEqualASCII(runes[i], needle[0]) {
			continue
		}
		match := true
		for j := 1; j < len(needle); j++ {
			if !foldRuneEqualASCII(runes[i+j], needle[j]) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		wordBefore := i > 0 && isNetWord(runes[i-1])
		wordAfter := i+len(needle) < len(runes) && isNetWord(runes[i+len(needle)])
		if !wordBefore && !wordAfter {
			occurrences = append(occurrences, i)
		}
	}
	return occurrences
}

func foldRuneEqualASCII(r, upper rune) bool {
	if r >= 'A' && r <= 'Z' {
		r += 'a' - 'A'
	}
	return r == upper|('a'-'A')
}
