package parityharness

import (
	"fmt"
	"strconv"
	"strings"
)

// Field-path syntax of classification annotations:
//
//   - "stats"                  — object member
//   - "findings[].rule"        — the member on every element of an array
//   - "inputs.review_sha256"   — nested members
//
// Paths address decoded canonical JSON (map[string]any, []any, json.Number).
// stripFields returns a deep copy with the addressed members removed and the
// number of removals per path. A schema-change field may legitimately exist
// in one engine only, so a path that removes nothing from one document is
// tolerated; the comparator requires every annotated path to remove
// something from at least one document, which is what prevents blanket
// masking.

type fieldSegment struct {
	name    string
	allItem bool
}

func parseFieldPath(path string) ([]fieldSegment, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("empty field path")
	}
	segments := []fieldSegment{}
	for index, raw := range strings.Split(path, ".") {
		if raw == "" {
			return nil, fmt.Errorf("field path %q has an empty segment at %d", path, index)
		}
		if !strings.HasSuffix(raw, "[]") {
			segments = append(segments, fieldSegment{name: raw})
			continue
		}
		name := strings.TrimSuffix(raw, "[]")
		if name == "" {
			return nil, fmt.Errorf("field path %q has an empty array member", path)
		}
		segments = append(segments, fieldSegment{name: name, allItem: true})
	}
	return segments, nil
}

// stripFields returns a deep copy of the document with every addressed
// member removed, plus the total number of removals per path. A structural
// mismatch (the path addresses a non-object where an object is required) is
// an error: annotations must name navigable fields.
func stripFields(document map[string]any, paths []string) (map[string]any, map[string]int, error) {
	clone, err := cloneDocument(document)
	if err != nil {
		return nil, nil, err
	}
	removals := map[string]int{}
	for _, path := range paths {
		count, err := stripPath(clone, path)
		if err != nil {
			return nil, nil, err
		}
		removals[path] = count
	}
	return clone, removals, nil
}

func stripPath(document map[string]any, path string) (int, error) {
	segments, err := parseFieldPath(path)
	if err != nil {
		return 0, err
	}
	if segments[len(segments)-1].allItem {
		return 0, fmt.Errorf("field path %q ends with an array wildcard", path)
	}
	removed := 0
	var walk func(object map[string]any, depth int) error
	walk = func(object map[string]any, depth int) error {
		segment := segments[depth]
		if segment.allItem {
			items, ok := object[segment.name].([]any)
			if !ok {
				return nil
			}
			for _, item := range items {
				child, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("field path %q addresses a non-object array item", path)
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if depth == len(segments)-1 {
			if _, ok := object[segment.name]; ok {
				delete(object, segment.name)
				removed++
			}
			return nil
		}
		if child, ok := object[segment.name].(map[string]any); ok {
			return walk(child, depth+1)
		}
		return nil
	}
	if err := walk(document, 0); err != nil {
		return 0, err
	}
	return removed, nil
}

func cloneDocument(document map[string]any) (map[string]any, error) {
	return cloneValue(document).(map[string]any), nil
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = cloneValue(item)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneValue(item)
		}
		return clone
	default:
		return value
	}
}

// formatValue renders one decoded JSON value compactly for diagnostics.
func formatValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", typed)
	}
}
