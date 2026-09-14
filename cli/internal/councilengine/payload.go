package councilengine

import (
	"regexp"
	"strings"
)

// payload.go ports Read-BSLFlowCouncilJsonResult (Council.Transport.ps1):
// strict single-JSON extraction of a role payload from model text. Whole
// object first, then one fenced block, then the balanced outer-object; never
// two candidates.

var fencedPattern = regexp.MustCompile(`(?s)` + "```" + `(?:json)?\s*(\{.*\})\s*` + "```")

// ParseCouncilPayload extracts exactly one JSON object from model text and
// returns it as an *ordered tree. It fails with a BF_INVALID_RESPONSE-class
// error on ambiguity.
func ParseCouncilPayload(rawText string) (*ordered, error) {
	trimmed := strings.TrimSpace(rawText)
	// Whole object first.
	if object, ok := tryParseObject(trimmed); ok {
		return object, nil
	}
	// Exactly one fenced block.
	fences := fencedPattern.FindAllStringSubmatch(trimmed, -1)
	if len(fences) > 1 {
		return nil, invalid("BF_INVALID_RESPONSE: ambiguous response with several JSON candidates.")
	}
	if len(fences) == 1 {
		object, ok := tryParseObject(fences[0][1])
		if !ok {
			return nil, invalid("BF_INVALID_RESPONSE: malformed fenced JSON payload.")
		}
		return object, nil
	}
	// Balanced outer-object extraction.
	first := strings.Index(trimmed, "{")
	if first >= 0 {
		inString := false
		escaped := false
		depth := 0
		end := -1
		for index := first; index < len(trimmed); index++ {
			character := trimmed[index]
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				if inString {
					escaped = true
				}
				continue
			}
			if character == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if character == '{' {
				depth++
			} else if character == '}' {
				depth--
				if depth == 0 {
					end = index
					break
				}
			}
		}
		if end > first {
			tail := strings.TrimSpace(trimmed[end+1:])
			if tail != "" && strings.Contains(tail, "{") {
				return nil, invalid("BF_INVALID_RESPONSE: response must contain exactly one JSON object.")
			}
			candidate := trimmed[first : end+1]
			object, ok := tryParseObject(candidate)
			if ok {
				return object, nil
			}
			// Repair raw control characters in long strings once.
			repaired := regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`).ReplaceAllStringFunc(candidate, func(m string) string {
				return `\u` + hex2(int(m[0]))
			})
			object, ok = tryParseObject(repaired)
			if !ok {
				return nil, invalid("BF_INVALID_RESPONSE: malformed JSON payload.")
			}
			return object, nil
		}
	}
	candidates := regexp.MustCompile(`\{[^{}]*\}`).FindAllString(trimmed, -1)
	if len(candidates) >= 2 {
		return nil, invalid("BF_INVALID_RESPONSE: response must contain exactly one JSON object.")
	}
	return nil, invalid("BF_INVALID_RESPONSE: malformed JSON payload.")
}

func hex2(value int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[value>>4], digits[value&0x0f]})
}

func tryParseObject(text string) (*ordered, bool) {
	data, err := decodeOrderedDocument([]byte(text))
	if err != nil {
		return nil, false
	}
	object, ok := data.(*ordered)
	if !ok {
		return nil, false
	}
	return object, true
}
