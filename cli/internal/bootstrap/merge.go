package bootstrap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Managed-block markers, ported from Initialize-BSLFlowProject.ps1:296-302
// and Update-BSLFlowProject.ps1:150-180.
const (
	gitIgnoreStartMarker = "# bsl-flow managed:start"
	gitIgnoreEndMarker   = "# bsl-flow managed:end"
	agentsStartMarker    = "<!-- bsl-flow managed:start -->"
	agentsEndMarker      = "<!-- bsl-flow managed:end -->"
)

var (
	yamlTabIndent      = regexp.MustCompile(`^\s*\t`)
	yamlBlankOrComment = regexp.MustCompile(`^\s*(?:#.*)?$`)
	yamlDocumentMarker = regexp.MustCompile(`^\s*(?:---|\.\.\.)\s*$`)
	yamlSequenceItem   = regexp.MustCompile(`^\s*-\s+`)
	yamlKeyLine        = regexp.MustCompile(`^( *)([A-Za-z_][A-Za-z0-9_-]*):(?:\s*(.*))?$`)
	yamlInlineComment  = regexp.MustCompile(`\s+#.*$`)
)

// lineSpan is one physical line: its content without the terminator, the
// byte offset of the line start and the terminator length (0 at end of file,
// 1 for "\n", 2 for "\r\n"). Working with spans keeps every other byte of
// the user file untouched.
type lineSpan struct {
	text  string
	start int
	term  int
}

func splitLines(text string) []lineSpan {
	var spans []lineSpan
	position := 0
	for position < len(text) {
		newline := strings.IndexByte(text[position:], '\n')
		if newline < 0 {
			spans = append(spans, lineSpan{text: text[position:], start: position, term: 0})
			break
		}
		end := position + newline
		contentEnd := end
		if contentEnd > position && text[contentEnd-1] == '\r' {
			contentEnd--
		}
		spans = append(spans, lineSpan{text: text[position:contentEnd], start: position, term: end + 1 - position})
		position = end + 1
	}
	return spans
}

// detectNewline returns the dominant line terminator of text; ties and texts
// without terminators resolve to "\n". The PowerShell contract detects CRLF
// by presence (Update-BSLFlowProject.ps1:18); the port uses the dominant
// terminator because a mixed file must keep its original bytes untouched and
// only the appended content adopts the majority ending.
func detectNewline(text string) string {
	crlf := strings.Count(text, "\r\n")
	loneLF := strings.Count(text, "\n") - crlf
	if crlf > loneLF {
		return "\r\n"
	}
	return "\n"
}

func endsWithLineBreak(text string) bool {
	return len(text) > 0 && text[len(text)-1] == '\n'
}

func markerLines(spans []lineSpan, marker, trimCutset string) []int {
	var found []int
	for i, span := range spans {
		if strings.TrimRight(span.text, trimCutset) == marker {
			found = append(found, i)
		}
	}
	return found
}

// singleManagedBlock locates the managed block between start and end marker
// lines. It returns -1, -1 when no marker exists at all. An incomplete,
// duplicate or out-of-order marker set is an error: the PowerShell scripts
// append another block in that case (Initialize-BSLFlowProject.ps1:297-300)
// or lazily replace across markers (Update-BSLFlowProject.ps1:152-153),
// which corrupts user content; the port blocks conservatively.
func singleManagedBlock(spans []lineSpan, startMarker, endMarker, trimCutset, label string) (int, int, error) {
	starts := markerLines(spans, startMarker, trimCutset)
	ends := markerLines(spans, endMarker, trimCutset)
	if len(starts) == 0 && len(ends) == 0 {
		return -1, -1, nil
	}
	if len(starts) == 1 && len(ends) == 1 && ends[0] > starts[0] {
		return starts[0], ends[0], nil
	}
	return -1, -1, fmt.Errorf("%s contains an incomplete or duplicate BSL Flow managed block", label)
}

func spanTexts(spans []lineSpan, from, to int) []string {
	texts := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		texts = append(texts, spans[i].text)
	}
	return texts
}

// blockSeparator returns the bytes separating existing content from an
// appended managed block: one blank line, after first terminating an
// unterminated last line.
func blockSeparator(current, newline string) string {
	if len(current) == 0 {
		return ""
	}
	if !endsWithLineBreak(current) {
		return newline + newline
	}
	return newline
}

// appendBlockBody appends the managed block to the end of current content
// separated by one blank line, matching Update-BSLFlowProject.ps1:154 and
// :171. Divergence: the PowerShell code trims every trailing line break of
// the user file first; the port never rewrites existing bytes, so trailing
// blank lines are preserved and the block is appended after them.
func appendBlockBody(current string, lines []string, newline string) string {
	return current + blockSeparator(current, newline) + strings.Join(lines, newline) + newline
}

// replaceManagedBlock swaps the byte range of the existing managed block
// (marker lines included, the end line's terminator preserved from the user
// file) for the packaged block joined with the file's detected newline.
// Update-BSLFlowProject.ps1:153 and :179 splice the template's original
// terminators instead, which mixes endings when template and project differ.
func replaceManagedBlock(current string, spans []lineSpan, start, end int, lines []string, newline string) string {
	prefix := current[:spans[start].start]
	suffix := current[spans[end].start+len(spans[end].text):]
	return prefix + strings.Join(lines, newline) + suffix
}

// mergeGitIgnoreText ports Get-UpdatedGitIgnore
// (Update-BSLFlowProject.ps1:150-155) onto line spans: an existing file
// without the managed block gets the packaged block appended at most once;
// an existing block that differs from the packaged template is replaced in
// place; an identical block leaves the file unchanged. The second return
// value describes the pending change (empty when none).
func mergeGitIgnoreText(current, template string) (string, string, error) {
	block := strings.TrimRight(template, "\r\n")
	if strings.TrimSpace(block) == "" {
		return "", "", fmt.Errorf("packaged .gitignore template is empty")
	}
	newline := detectNewline(current)
	blockSpans := splitLines(block)
	blockLines := spanTexts(blockSpans, 0, len(blockSpans)-1)
	spans := splitLines(current)
	start, end, err := singleManagedBlock(spans, gitIgnoreStartMarker, gitIgnoreEndMarker, " \t\r\v\f", ".gitignore")
	if err != nil {
		return "", "", err
	}
	if start < 0 {
		return appendBlockBody(current, blockLines, newline), "managed block is absent", nil
	}
	if strings.Join(spanTexts(spans, start, end), "\n") == strings.Join(blockLines, "\n") {
		return current, "", nil
	}
	return replaceManagedBlock(current, spans, start, end, blockLines, newline), "managed block differs from the packaged template", nil
}

// mergeAgentsText ports Get-UpdatedAgents (Update-BSLFlowProject.ps1:157-180)
// onto line spans: a file without the managed comment block gets only the
// block appended (never the whole template); an existing block that differs
// is replaced in place; comparison ignores line-ending differences exactly
// like the PowerShell normalization on lines 176-178.
func mergeAgentsText(current, template string) (string, string, error) {
	templateSpans := splitLines(template)
	tStart, tEnd, err := singleManagedBlock(templateSpans, agentsStartMarker, agentsEndMarker, " \t\r", "packaged AGENTS.md template")
	if err != nil {
		return "", "", err
	}
	if tStart < 0 {
		return "", "", fmt.Errorf("packaged AGENTS.md template must contain exactly one complete BSL Flow managed block")
	}
	blockLines := spanTexts(templateSpans, tStart, tEnd)
	newline := detectNewline(current)
	spans := splitLines(current)
	start, end, err := singleManagedBlock(spans, agentsStartMarker, agentsEndMarker, " \t\r", "AGENTS.md")
	if err != nil {
		return "", "", err
	}
	if start < 0 {
		return appendBlockBody(current, blockLines, newline), "managed block is absent", nil
	}
	if strings.Join(spanTexts(spans, start, end), "\n") == strings.Join(blockLines, "\n") {
		return current, "", nil
	}
	return replaceManagedBlock(current, spans, start, end, blockLines, newline), "managed block differs from the packaged template", nil
}

// yamlEntry is one mapping key from the line-based YAML map, ported from
// Get-YamlMap (Update-BSLFlowProject.ps1:64-91).
type yamlEntry struct {
	path   string
	indent int
	line   int
	isMap  bool
}

type yamlDoc struct {
	lines   []lineSpan
	entries map[string]yamlEntry
	order   []string
}

// parseYAMLMap ports Get-YAMLMap: a strict line-based reader for the managed
// config dialect (two-space indentation, no tabs, no duplicate key paths).
// Unsupported YAML is an error, which blocks the merge instead of risking a
// lossy rewrite - the same conservative behavior as the PowerShell script.
func parseYAMLMap(text, label string) (*yamlDoc, error) {
	spans := splitLines(text)
	doc := &yamlDoc{lines: spans, entries: make(map[string]yamlEntry)}
	type frame struct {
		indent int
		path   string
	}
	var stack []frame
	for i, span := range spans {
		line := span.text
		switch {
		case yamlTabIndent.MatchString(line):
			return nil, fmt.Errorf("%s contains tab indentation at line %d", label, i+1)
		case yamlBlankOrComment.MatchString(line), yamlDocumentMarker.MatchString(line), yamlSequenceItem.MatchString(line):
			continue
		}
		match := yamlKeyLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("%s contains unsupported YAML at line %d: %s", label, i+1, line)
		}
		indent := len(match[1])
		if indent%2 != 0 {
			return nil, fmt.Errorf("%s uses unsupported odd indentation at line %d", label, i+1)
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if indent > 0 && (len(stack) == 0 || stack[len(stack)-1].indent != indent-2) {
			return nil, fmt.Errorf("%s has an unsupported indentation jump at line %d", label, i+1)
		}
		key := match[2]
		path := key
		if len(stack) > 0 {
			path = stack[len(stack)-1].path + "." + key
		}
		if _, exists := doc.entries[path]; exists {
			return nil, fmt.Errorf("%s contains duplicate key path '%s'", label, path)
		}
		value := strings.TrimSpace(yamlInlineComment.ReplaceAllString(match[3], ""))
		entry := yamlEntry{path: path, indent: indent, line: i, isMap: value == ""}
		doc.entries[path] = entry
		doc.order = append(doc.order, path)
		if entry.isMap {
			stack = append(stack, frame{indent: indent, path: path})
		}
	}
	return doc, nil
}

func leadingSpaces(line string) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}

// subtreeLines ports Get-Subtree (Update-BSLFlowProject.ps1:93-103): the
// template lines of a mapping entry including nested comments and sequence
// items, up to (excluding) the first following line that closes the block.
func subtreeLines(doc *yamlDoc, entry yamlEntry) []string {
	end := len(doc.lines)
	for i := entry.line + 1; i < len(doc.lines); i++ {
		line := doc.lines[i].text
		if yamlBlankOrComment.MatchString(line) {
			continue
		}
		if leadingSpaces(line) <= entry.indent {
			end = i
			break
		}
	}
	return spanTexts(doc.lines, entry.line, end-1)
}

// parentInsertIndex ports the insertion-point scan of Add-MissingTemplateNodes
// (Update-BSLFlowProject.ps1:127-133): the first non-blank, non-comment line
// that closes the parent mapping, or end of file.
func parentInsertIndex(doc *yamlDoc, parent yamlEntry) int {
	for i := parent.line + 1; i < len(doc.lines); i++ {
		line := doc.lines[i].text
		if yamlBlankOrComment.MatchString(line) {
			continue
		}
		if leadingSpaces(line) <= parent.indent {
			return i
		}
	}
	return len(doc.lines)
}

// mergeBSLFlowYAML ports Add-MissingTemplateNodes
// (Update-BSLFlowProject.ps1:105-141) onto byte-offset splices:
//
//   - every template key path already present keeps its user value, comment
//     and position byte-identical (only missing paths are inserted);
//   - a missing top-level key is appended to the end of the file, separated
//     by one blank line, with its whole template subtree including comments;
//   - a missing nested key is inserted at the end of its parent mapping
//     block, immediately before the first closing line;
//   - a key whose parent does not exist yet is skipped (it arrives with the
//     ancestor subtree);
//   - a template mapping colliding with a user scalar is an error, and so is
//     a non-mapping parent - the merge blocks instead of rewriting.
//
// Divergence: the PowerShell script re-parses and re-joins the whole file
// after every insertion, which can normalize mixed line endings; the port
// splices at byte offsets, so every user byte outside the inserted ranges is
// preserved exactly, and inserted lines use the file's dominant terminator.
func mergeBSLFlowYAML(currentText, templateText string) (string, string, error) {
	current, err := parseYAMLMap(currentText, "bsl-flow.yaml")
	if err != nil {
		return "", "", err
	}
	template, err := parseYAMLMap(templateText, "packaged bsl-flow.yaml template")
	if err != nil {
		return "", "", err
	}
	newline := detectNewline(currentText)
	type insertion struct {
		offset int
		body   string
		index  int
	}
	effective := make(map[string]bool, len(current.entries))
	for path := range current.entries {
		effective[path] = true
	}
	var insertions []insertion
	var added []string
	for index, path := range template.order {
		wanted := template.entries[path]
		if effective[path] {
			if have, ok := current.entries[path]; ok && wanted.isMap && !have.isMap {
				return "", "", fmt.Errorf("managed path '%s' must be a mapping, but the project contains a scalar", path)
			}
			continue
		}
		parentPath := ""
		if cut := strings.LastIndex(path, "."); cut >= 0 {
			parentPath = path[:cut]
		}
		if parentPath != "" && !effective[parentPath] {
			continue
		}
		subtree := subtreeLines(template, wanted)
		if !wanted.isMap {
			subtree = subtree[:1]
		}
		var body string
		var offset int
		if parentPath == "" {
			offset = len(currentText)
			body = blockSeparator(currentText, newline) + strings.Join(subtree, newline) + newline
		} else {
			parent, ok := current.entries[parentPath]
			if !ok {
				continue
			}
			if !parent.isMap {
				return "", "", fmt.Errorf("managed parent '%s' is not a mapping", parentPath)
			}
			closer := parentInsertIndex(current, parent)
			if closer < len(current.lines) {
				offset = current.lines[closer].start
			} else {
				offset = len(currentText)
			}
			body = strings.Join(subtree, newline) + newline
			if offset == len(currentText) && !endsWithLineBreak(currentText) {
				body = newline + body
			}
		}
		insertions = append(insertions, insertion{offset: offset, body: body, index: index})
		added = append(added, path)
		effective[path] = true
		if wanted.isMap {
			for _, descendant := range template.order {
				if strings.HasPrefix(descendant, path+".") {
					effective[descendant] = true
				}
			}
		}
	}
	if len(insertions) == 0 {
		return currentText, "", nil
	}
	// Splice from the largest offset down so earlier offsets stay valid; at
	// equal offsets the later template entry is applied first, leaving the
	// entries in template order in the result.
	sort.SliceStable(insertions, func(i, j int) bool {
		if insertions[i].offset != insertions[j].offset {
			return insertions[i].offset > insertions[j].offset
		}
		return insertions[i].index > insertions[j].index
	})
	result := currentText
	for _, ins := range insertions {
		result = result[:ins.offset] + ins.body + result[ins.offset:]
	}
	return result, "missing managed keys: " + strings.Join(added, ", "), nil
}
