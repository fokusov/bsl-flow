package memoryhost

// This file ports Test-BFJsonSyntax from Task.Storage.ps1: a strict,
// value-counted scanner that classifies the top-level JSON kind, rejects
// duplicate object members (case-insensitively, like the PowerShell
// HashSet with StringComparer.OrdinalIgnoreCase) and carries the exact
// scanner diagnostics that surface in memory blocker text.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	jsonMaximumDepthValue  = 100
	jsonMaximumValuesCount = 1000000
)

var jsonNumberPrefix = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?`)

// testJSONSyntax validates one complete JSON document and returns its
// top-level kind ("object", "array" or "scalar").
func testJSONSyntax(text string) (string, error) {
	scanner := &jsonScanner{text: text}
	kind, err := scanner.parseValue(0)
	if err != nil {
		return "", err
	}
	scanner.skipWhitespace()
	if scanner.index != len(scanner.text) {
		return "", newBFError("BF_INVALID", "Unexpected data after JSON value.")
	}
	return kind, nil
}

type jsonScanner struct {
	text string
	// lastString carries the most recently scanned member name so the object
	// loop can register it for duplicate detection, mirroring the PowerShell
	// closure state.
	index      int
	values     int
	lastString string
}

func (s *jsonScanner) skipWhitespace() {
	for s.index < len(s.text) && strings.ContainsRune(" \t\r\n", rune(s.text[s.index])) {
		s.index++
	}
}

func (s *jsonScanner) parseValue(depth int) (string, error) {
	if depth > jsonMaximumDepthValue {
		return "", newBFError("BF_INVALID", "JSON exceeds the maximum nesting depth.")
	}
	s.values++
	if s.values > jsonMaximumValuesCount {
		return "", newBFError("BF_INVALID", "JSON contains too many values.")
	}
	s.skipWhitespace()
	if s.index >= len(s.text) {
		return "", newBFError("BF_INVALID", "Expected a JSON value.")
	}
	switch s.text[s.index] {
	case '"':
		if _, err := s.parseString(); err != nil {
			return "", err
		}
		return "scalar", nil
	case '{':
		s.index++
		s.skipWhitespace()
		keys := make(map[string]struct{})
		if s.index < len(s.text) && s.text[s.index] == '}' {
			s.index++
			return "object", nil
		}
		for {
			s.skipWhitespace()
			key, err := s.parseString()
			if err != nil {
				return "", err
			}
			folded := strings.ToLower(key)
			if _, duplicate := keys[folded]; duplicate {
				return "", newBFError("BF_INVALID", fmt.Sprintf("Duplicate JSON object key: %s.", key))
			}
			keys[folded] = struct{}{}
			s.skipWhitespace()
			if s.index >= len(s.text) || s.text[s.index] != ':' {
				return "", newBFError("BF_INVALID", "Expected a colon after JSON object key.")
			}
			s.index++
			if _, err := s.parseValue(depth + 1); err != nil {
				return "", err
			}
			s.skipWhitespace()
			if s.index < len(s.text) && s.text[s.index] == ',' {
				s.index++
				continue
			}
			if s.index < len(s.text) && s.text[s.index] == '}' {
				s.index++
				return "object", nil
			}
			return "", newBFError("BF_INVALID", "Expected comma or closing brace in JSON object.")
		}
	case '[':
		s.index++
		s.skipWhitespace()
		if s.index < len(s.text) && s.text[s.index] == ']' {
			s.index++
			return "array", nil
		}
		for {
			if _, err := s.parseValue(depth + 1); err != nil {
				return "", err
			}
			s.skipWhitespace()
			if s.index < len(s.text) && s.text[s.index] == ',' {
				s.index++
				continue
			}
			if s.index < len(s.text) && s.text[s.index] == ']' {
				s.index++
				return "array", nil
			}
			return "", newBFError("BF_INVALID", "Expected comma or closing bracket in JSON array.")
		}
	}
	for _, literal := range []string{"true", "false", "null"} {
		if s.index+len(literal) <= len(s.text) && s.text[s.index:s.index+len(literal)] == literal {
			s.index += len(literal)
			return "scalar", nil
		}
	}
	match := jsonNumberPrefix.FindString(s.text[s.index:])
	if match == "" {
		return "", newBFError("BF_INVALID", "Invalid JSON value.")
	}
	s.index += len(match)
	return "scalar", nil
}

func (s *jsonScanner) parseString() (string, error) {
	if s.index >= len(s.text) || s.text[s.index] != '"' {
		return "", newBFError("BF_INVALID", "Expected a JSON string.")
	}
	s.index++
	var builder strings.Builder
	for s.index < len(s.text) {
		character := s.text[s.index]
		s.index++
		if character == '"' {
			s.lastString = builder.String()
			return builder.String(), nil
		}
		if character < 0x20 {
			return "", newBFError("BF_INVALID", "Unescaped control character in JSON string.")
		}
		if character != '\\' {
			builder.WriteByte(character)
			continue
		}
		if s.index >= len(s.text) {
			return "", newBFError("BF_INVALID", "Incomplete JSON escape.")
		}
		escape := s.text[s.index]
		s.index++
		switch escape {
		case '"':
			builder.WriteByte('"')
		case '\\':
			builder.WriteByte('\\')
		case '/':
			builder.WriteByte('/')
		case 'b':
			builder.WriteByte('\b')
		case 'f':
			builder.WriteByte('\f')
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		case 'u':
			if s.index+4 > len(s.text) {
				return "", newBFError("BF_INVALID", "Incomplete JSON Unicode escape.")
			}
			hex := s.text[s.index : s.index+4]
			value, err := strconv.ParseUint(hex, 16, 32)
			if err != nil {
				return "", newBFError("BF_INVALID", "Invalid JSON Unicode escape.")
			}
			builder.WriteRune(rune(value))
			s.index += 4
		default:
			return "", newBFError("BF_INVALID", "Invalid JSON escape.")
		}
	}
	return "", newBFError("BF_INVALID", "Unterminated JSON string.")
}
