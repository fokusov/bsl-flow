package councilengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// psjson.go is the byte-parity replicator of PowerShell 7 ConvertTo-Json for
// the council artifact domain, verified against live pwsh 7.6 (see
// .bsl-flow/reports/w7-json-format-spike.md):
//
//   - pretty formatting: two-space indent, ": " key separator, CRLF line
//     breaks, no trailing newline;
//   - object members keep insertion order (the engine builds every artifact
//     through insertion-ordered values; key-sorted output would diverge);
//   - strings: raw UTF-8, only `"` and `\` and control characters below 0x20
//     escaped (`\u00xx`, lowercase hex), no HTML escaping;
//   - numbers: integer literals round-trip; double values use the shortest
//     round-trip form with .NET-style fixed/exponent switching
//     (see formatPSDouble);
//   - Compress: no whitespace at all, same escaping.
//
// Every persisted council artifact and every hashed byte string in this
// package goes through these functions; repository.Canonical (key-sorted, LF)
// is intentionally not used for them.

// orderedValue is a JSON value that keeps explicit member order for objects.
// The PowerShell engine builds artifacts as [ordered]@{}/[pscustomobject] and
// ConvertTo-Json preserves that insertion order, so the Go port carries the
// order explicitly instead of relying on map iteration.
type orderedValue interface{}

// ordered is one insertion-ordered JSON object.
type ordered struct {
	keys   []string
	values map[string]any
}

// newOrdered creates an empty insertion-ordered object.
func newOrdered() *ordered {
	return &ordered{values: map[string]any{}}
}

// orderedFrom pairs keys with values positionally; len must match.
func orderedFrom(keys []string, values []any) *ordered {
	object := newOrdered()
	for index, key := range keys {
		object.set(key, values[index])
	}
	return object
}

func (o *ordered) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *ordered) get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

func (o *ordered) has(key string) bool {
	if o == nil {
		return false
	}
	_, exists := o.values[key]
	return exists
}

func (o *ordered) keysOf() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Ordered exposes the insertion-ordered JSON object to host consumers of the
// engine (the concrete type stays unexported; the alias keeps the pointer
// shape usable across the package boundary).
type Ordered = ordered

// AsOrdered exposes asOrdered to host consumers.
func AsOrdered(value any) (*Ordered, bool) { return asOrdered(value) }

// Get returns the member value or nil (exported accessor for host consumers).
func (o *ordered) Get(key string) any { return o.get(key) }

// OrderedToMap deep-converts an insertion-ordered object into a plain
// map[string]any tree (numbers stay json.Number, arrays become []any), so
// host consumers can feed the review back into their map accessors.
func OrderedToMap(value *Ordered) map[string]any {
	if value == nil {
		return nil
	}
	data, err := convertToJSON(value, 20)
	if err != nil {
		return nil
	}
	object, err := decodeObjectDocument(data)
	if err != nil {
		return nil
	}
	return object
}

// Exported psjson builders and encoders for sibling packages that must emit
// the same PowerShell ConvertTo-Json byte shape (the single-reviewer route,
// the metric record and the final-validation receipt).

// NewOrdered creates an empty insertion-ordered object.
func NewOrdered() *Ordered { return newOrdered() }

// OrderedFrom pairs keys with values positionally (len must match).
func OrderedFrom(keys []string, values []any) *Ordered { return orderedFrom(keys, values) }

// Set inserts or overwrites a member, preserving first-seen key order.
func (o *ordered) Set(key string, value any) { o.set(key, value) }

// Has reports whether the member exists.
func (o *ordered) Has(key string) bool { return o.has(key) }

// Keys returns the member keys in insertion order.
func (o *ordered) Keys() []string { return o.keysOf() }

// ConvertToJSON renders value like ConvertTo-Json -Depth depth (pretty).
func ConvertToJSON(value any, depth int) ([]byte, error) { return convertToJSON(value, depth) }

// ConvertToJSONCompress renders value like ConvertTo-Json -Compress.
func ConvertToJSONCompress(value any, depth int) ([]byte, error) {
	return convertToJSONCompress(value, depth)
}

// WriteJSONAtomic ports Write-BSLFlowJsonAtomic: pretty ConvertTo-Json bytes
// plus one trailing CRLF, published atomically.
func WriteJSONAtomic(path string, value any) error { return writeBSLFlowJSONAtomic(path, value) }

// Sha256Hex returns the lowercase hex digest of bytes.
func Sha256Hex(data []byte) string { return sha256Hex(data) }

// asOrdered converts an arbitrary decoded JSON object (map[string]any or
// *ordered) into *ordered. Key order of a plain map is its sorted order —
// only relevant when materializing artifacts that were themselves produced by
// this engine (they keep order) or parsed from engine-written files.
func asOrdered(value any) (*ordered, bool) {
	switch typed := value.(type) {
	case *ordered:
		return typed, true
	case map[string]any:
		object := newOrdered()
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			object.set(key, typed[key])
		}
		return object, true
	default:
		return nil, false
	}
}

// convertToJSON renders value exactly like PowerShell ConvertTo-Json -Depth
// depth (pretty formatting). The returned bytes have no trailing newline.
// The depth bound itself is advisory in PowerShell 7 (overruns warn instead
// of failing); the engine always calls with sufficient depth, so no depth
// error is raised here either.
func convertToJSON(value any, depth int) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writePSJSON(&buffer, value, 0, depth); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// convertToJSONCompress renders value exactly like ConvertTo-Json -Compress.
func convertToJSONCompress(value any, depth int) ([]byte, error) {
	pretty, err := convertToJSON(value, depth)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := compactPSJSON(&compact, pretty); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func compactPSJSON(buffer *bytes.Buffer, pretty []byte) error {
	depth := 0
	inString := false
	escaped := false
	for index := 0; index < len(pretty); index++ {
		character := pretty[index]
		if inString {
			buffer.WriteByte(character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			buffer.WriteByte(character)
			inString = true
		case ' ', '\r', '\n', '\t':
			// structural whitespace between tokens
			continue
		case ':':
			buffer.WriteString(":")
		case ',':
			buffer.WriteString(",")
		case '{', '[':
			buffer.WriteByte(character)
			depth++
		case '}', ']':
			buffer.WriteByte(character)
			depth--
		default:
			buffer.WriteByte(character)
		}
	}
	if inString {
		return errors.New("councilengine: unterminated string while compacting JSON")
	}
	return nil
}

func writePSJSON(buffer *bytes.Buffer, value any, level, depth int) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		writePSString(buffer, typed)
	case json.Number:
		text := string(typed)
		if !validPSNumber(text) {
			return fmt.Errorf("councilengine: invalid number literal %q", text)
		}
		buffer.WriteString(text)
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		buffer.WriteString(formatPSDouble(typed))
	case []any:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		if len(typed) == 0 {
			buffer.WriteString("[]")
			return nil
		}
		buffer.WriteString("[\r\n")
		for index, item := range typed {
			if index > 0 {
				buffer.WriteString(",\r\n")
			}
			writeIndent(buffer, level+1)
			if err := writePSJSON(buffer, item, level+1, depth); err != nil {
				return err
			}
		}
		buffer.WriteString("\r\n")
		writeIndent(buffer, level)
		buffer.WriteString("]")
	case *ordered:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		if len(typed.keys) == 0 {
			buffer.WriteString("{}")
			return nil
		}
		buffer.WriteString("{\r\n")
		for index, key := range typed.keys {
			if index > 0 {
				buffer.WriteString(",\r\n")
			}
			writeIndent(buffer, level+1)
			writePSString(buffer, key)
			buffer.WriteString(": ")
			if err := writePSJSON(buffer, typed.values[key], level+1, depth); err != nil {
				return err
			}
		}
		buffer.WriteString("\r\n")
		writeIndent(buffer, level)
		buffer.WriteString("}")
	case map[string]any:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		converted, _ := asOrdered(typed)
		return writePSJSON(buffer, converted, level, depth)
	default:
		return fmt.Errorf("councilengine: unsupported JSON value type %T", value)
	}
	return nil
}

func writeIndent(buffer *bytes.Buffer, level int) {
	for index := 0; index < level; index++ {
		buffer.WriteString("  ")
	}
}

// writePSString mirrors PowerShell 7 ConvertTo-Json string escaping: only
// `"`, `\` and control characters below 0x20 are escaped; control characters
// use `\u00xx` with lowercase hex (except the named \b \f \n \r \t short
// forms); everything else is raw UTF-8. Verified against live pwsh.
func writePSString(buffer *bytes.Buffer, value string) {
	if !utf8.ValidString(value) {
		// PowerShell cannot hold invalid UTF-16 in its strings; the Go port
		// refuses instead of silently encoding lone bytes.
		buffer.WriteString(`"invalid utf-8"`)
		return
	}
	buffer.WriteByte('"')
	for index := 0; index < len(value); {
		character := value[index]
		if character < 0x20 || character == '"' || character == '\\' {
			switch character {
			case '\b':
				buffer.WriteString(`\b`)
			case '\f':
				buffer.WriteString(`\f`)
			case '\n':
				buffer.WriteString(`\n`)
			case '\r':
				buffer.WriteString(`\r`)
			case '\t':
				buffer.WriteString(`\t`)
			case '"':
				buffer.WriteString(`\"`)
			case '\\':
				buffer.WriteString(`\\`)
			default:
				fmt.Fprintf(buffer, `\u%04x`, character)
			}
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == utf8.RuneError && size == 1 {
			buffer.WriteString(`"invalid utf-8"`)
			return
		}
		buffer.WriteString(value[index : index+size])
		index += size
	}
	buffer.WriteByte('"')
}

// formatPSDouble renders a float64 exactly like PowerShell 7 ConvertTo-Json
// (the .NET shortest round-trip form): integral doubles in fixed notation
// keep ".0"; fixed notation covers 1e-4 <= |v| < 1e17 (verified boundaries:
// 0.0001 fixes, 9.9999e-5 goes exponent, 9.99e16 fixes, 1e17 goes exponent);
// outside it the exponent form uses an uppercase E, an always-present sign
// and at least two exponent digits (1E+21, 1.234E-06,
// 1.7976931348623157E+308).
func formatPSDouble(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		// ConvertTo-Json rejects these; the engine never persists them.
		return "0.0"
	}
	sign := ""
	if math.Signbit(value) {
		sign = "-"
		value = -value
	}
	if value == 0 {
		return sign + "0.0"
	}
	// Shortest round-trip digits (Go's Ryū encoder produces the same digit
	// sequence as .NET's for every double).
	digits, exponent := strconv.FormatFloat(value, 'e', -1, 64), 0
	mantissa := digits
	if position := strings.IndexAny(digits, "eE"); position >= 0 {
		mantissa = digits[:position]
		parsed, _ := strconv.Atoi(digits[position+1:])
		exponent = parsed
	}
	mantissa = strings.Replace(mantissa, ".", "", 1)
	// exponent here is the power-of-ten of the FIRST digit (1.5e+21 -> 21).
	fixed := exponent >= -4 && exponent < 17
	if fixed {
		return sign + fixedFromDigits(mantissa, exponent)
	}
	return sign + exponentFromDigits(mantissa, exponent)
}

func fixedFromDigits(digits string, exponent int) string {
	var builder strings.Builder
	if exponent >= 0 {
		if len(digits) > exponent+1 {
			builder.WriteString(digits[:exponent+1])
			builder.WriteString(".")
			builder.WriteString(digits[exponent+1:])
		} else {
			builder.WriteString(digits)
			builder.WriteString(strings.Repeat("0", exponent+1-len(digits)))
			builder.WriteString(".0")
		}
		return builder.String()
	}
	builder.WriteString("0.")
	builder.WriteString(strings.Repeat("0", -exponent-1))
	builder.WriteString(digits)
	return builder.String()
}

func exponentFromDigits(digits string, exponent int) string {
	var builder strings.Builder
	builder.WriteByte(digits[0])
	if len(digits) > 1 {
		builder.WriteString(".")
		builder.WriteString(digits[1:])
	}
	builder.WriteString("E")
	if exponent >= 0 {
		builder.WriteString("+")
	} else {
		builder.WriteString("-")
		exponent = -exponent
	}
	if exponent < 10 {
		builder.WriteString("0")
	}
	builder.WriteString(strconv.Itoa(exponent))
	return builder.String()
}

func validPSNumber(text string) bool {
	if text == "" {
		return false
	}
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return false
	}
	// Reject forms Go accepts but JSON does not (hex, underscores).
	for index := 0; index < len(text); index++ {
		character := text[index]
		if (character >= '0' && character <= '9') || character == '+' || character == '-' ||
			character == '.' || character == 'e' || character == 'E' {
			continue
		}
		return false
	}
	return true
}
