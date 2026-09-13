package repository

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	// ReadFileBytes bounds the report passed by the controller. These parser
	// bounds keep malformed XML from consuming an unbounded stack or element
	// counter while still allowing ordinary JUnit reports.
	nativeJUnitMaxDepth    = 256
	nativeJUnitMaxElements = 1_000_000
)

var nativeJUnitAggregateNames = [...]string{"tests", "failures", "errors", "skipped", "disabled"}

type nativeJUnitSubtreeCounts struct {
	tests    int64
	failures int64
	errors   int64
	skipped  int64
	disabled int64
}

type nativeJUnitFrame struct {
	name          xml.Name
	isSuite       bool
	isTestcase    bool
	testcaseName  string
	directFailure bool
	directError   bool
	aggregates    map[string]string
	subtreeCounts nativeJUnitSubtreeCounts
}

// validateNativeJUnit validates the bounded JUnit report used by a native
// criterion. It streams the XML and keeps only the open-element stack, so
// subtree aggregate counts do not require building an attacker-controlled DOM.
// A structurally valid report containing known failure/error elements returns
// (false, nil); malformed, unsafe, incomplete, or mismatched evidence returns
// an error.
func validateNativeJUnit(data []byte, expected []string) (bool, error) {
	data, err := normalizeNativeJUnitXML(data)
	if err != nil {
		return false, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	decoder.CharsetReader = nativeJUnitCharsetReader

	stack := make([]nativeJUnitFrame, 0, 8)
	actualNames := make([]string, 0, len(expected))
	seenNames := make(map[string]struct{}, len(expected))
	rootSeen := false
	rootClosed := false
	sawFailure := false
	sawSkipped := false
	elementCount := 0

	for {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			if errors.Is(tokenErr, io.EOF) {
				break
			}
			return false, fmt.Errorf("malformed or unsafe JUnit report: %w", tokenErr)
		}
		switch value := token.(type) {
		case xml.StartElement:
			elementCount++
			if elementCount > nativeJUnitMaxElements {
				return false, fmt.Errorf("JUnit report exceeds %d elements", nativeJUnitMaxElements)
			}
			if len(stack) >= nativeJUnitMaxDepth {
				return false, fmt.Errorf("JUnit report nesting exceeds %d levels", nativeJUnitMaxDepth)
			}
			if value.Name.Space != "" {
				return false, fmt.Errorf("JUnit report namespaces are not supported: %s", value.Name.Local)
			}
			if rootClosed {
				return false, errors.New("JUnit report contains multiple root elements")
			}
			if !rootSeen {
				rootSeen = true
				if value.Name.Local != "testsuite" && value.Name.Local != "testsuites" {
					return false, fmt.Errorf("unsupported JUnit root %q", value.Name.Local)
				}
			}
			aggregates, attributesErr := nativeJUnitAttributes(value.Attr)
			if attributesErr != nil {
				return false, attributesErr
			}
			frame := nativeJUnitFrame{
				name:       value.Name,
				isSuite:    value.Name.Local == "testsuite" || value.Name.Local == "testsuites",
				isTestcase: value.Name.Local == "testcase",
				aggregates: aggregates,
			}
			if frame.isTestcase {
				name, present := nativeJUnitAttribute(value.Attr, "name")
				if !present || name == "" {
					return false, errors.New("JUnit testcase name is missing")
				}
				if _, duplicate := seenNames[name]; duplicate {
					return false, fmt.Errorf("duplicate JUnit testcase name %q", name)
				}
				seenNames[name] = struct{}{}
				actualNames = append(actualNames, name)
				frame.testcaseName = name
			}
			if value.Name.Local == "failure" {
				sawFailure = true
				if len(stack) > 0 && stack[len(stack)-1].isTestcase {
					stack[len(stack)-1].directFailure = true
				}
			}
			if value.Name.Local == "error" {
				sawFailure = true
				if len(stack) > 0 && stack[len(stack)-1].isTestcase {
					stack[len(stack)-1].directError = true
				}
			}
			if value.Name.Local == "skipped" {
				sawSkipped = true
				nativeJUnitAddToSuites(stack, func(counts *nativeJUnitSubtreeCounts) { counts.skipped++ })
			}
			if value.Name.Local == "disabled" {
				nativeJUnitAddToSuites(stack, func(counts *nativeJUnitSubtreeCounts) { counts.disabled++ })
			}
			stack = append(stack, frame)

		case xml.EndElement:
			if len(stack) == 0 {
				return false, errors.New("malformed JUnit report: unexpected closing element")
			}
			frameIndex := len(stack) - 1
			frame := stack[frameIndex]
			if value.Name.Space != "" || value.Name != frame.name {
				return false, fmt.Errorf("malformed JUnit report: closing element %q does not match %q", value.Name.Local, frame.name.Local)
			}
			if frame.isTestcase {
				nativeJUnitAddToSuites(stack[:frameIndex], func(counts *nativeJUnitSubtreeCounts) {
					counts.tests++
					if frame.directFailure {
						counts.failures++
					}
					if frame.directError {
						counts.errors++
					}
				})
			}
			if frame.isSuite {
				if aggregateErr := nativeJUnitCheckAggregates(frame); aggregateErr != nil {
					return false, aggregateErr
				}
			}
			stack = stack[:frameIndex]
			if len(stack) == 0 {
				rootClosed = true
			}

		case xml.CharData:
			if (len(stack) == 0) && strings.TrimSpace(string(value)) != "" {
				return false, errors.New("malformed JUnit report: non-whitespace data outside root")
			}

		case xml.Directive:
			return false, errors.New("unsafe JUnit report directive or DTD")

		case xml.ProcInst:
			// The XML declaration is harmless and is needed for encoding
			// detection. Other processing instructions are not part of the
			// closed JUnit evidence format.
			if value.Target != "xml" {
				return false, fmt.Errorf("unsafe JUnit processing instruction %q", value.Target)
			}

		case xml.Comment:
			// Comments carry no report semantics.
		}
	}

	if !rootSeen || !rootClosed || len(stack) != 0 {
		return false, errors.New("malformed JUnit report: root element is incomplete")
	}
	if sawSkipped {
		return false, errors.New("required tests were skipped")
	}
	if err := nativeJUnitMatchExpected(actualNames, expected); err != nil {
		return false, err
	}
	if sawFailure {
		return false, nil
	}
	return true, nil
}

func nativeJUnitAttributes(attributes []xml.Attr) (map[string]string, error) {
	seen := make(map[string]struct{}, len(attributes))
	aggregates := make(map[string]string, len(nativeJUnitAggregateNames))
	for _, attribute := range attributes {
		key := attribute.Name.Space + "\x00" + attribute.Name.Local
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("malformed JUnit report: duplicate attribute %q", attribute.Name.Local)
		}
		seen[key] = struct{}{}
		if attribute.Name.Space == "" {
			for _, aggregate := range nativeJUnitAggregateNames {
				if attribute.Name.Local == aggregate {
					aggregates[aggregate] = attribute.Value
					break
				}
			}
		}
	}
	return aggregates, nil
}

func nativeJUnitAttribute(attributes []xml.Attr, name string) (string, bool) {
	for _, attribute := range attributes {
		if attribute.Name.Space == "" && attribute.Name.Local == name {
			return attribute.Value, true
		}
	}
	return "", false
}

func nativeJUnitAddToSuites(stack []nativeJUnitFrame, update func(*nativeJUnitSubtreeCounts)) {
	for index := range stack {
		if stack[index].isSuite {
			update(&stack[index].subtreeCounts)
		}
	}
}

func nativeJUnitCheckAggregates(frame nativeJUnitFrame) error {
	for _, name := range nativeJUnitAggregateNames {
		value, present := frame.aggregates[name]
		if !present {
			continue
		}
		observed, parseErr := nativeJUnitDigits(value)
		if parseErr != nil {
			return fmt.Errorf("invalid JUnit %s aggregate: %w", name, parseErr)
		}
		want := nativeJUnitAggregateValue(frame.subtreeCounts, name)
		if observed != want {
			return fmt.Errorf("inconsistent JUnit %s aggregate: got %d, want %d", name, observed, want)
		}
	}
	return nil
}

func nativeJUnitAggregateValue(counts nativeJUnitSubtreeCounts, name string) int64 {
	switch name {
	case "tests":
		return counts.tests
	case "failures":
		return counts.failures
	case "errors":
		return counts.errors
	case "skipped":
		return counts.skipped
	case "disabled":
		return counts.disabled
	default:
		return 0
	}
}

func nativeJUnitDigits(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("value must be ASCII decimal digits without leading zeroes")
	}
	var result int64
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("value must be ASCII decimal digits without leading zeroes")
		}
		digit := int64(character - '0')
		if result > (int64(^uint64(0)>>1)-digit)/10 {
			return 0, errors.New("value exceeds the supported integer range")
		}
		result = result*10 + digit
	}
	return result, nil
}

func nativeJUnitMatchExpected(actual, expected []string) error {
	if len(actual) == 0 || len(actual) != len(expected) {
		return errors.New("JUnit selection does not match exact expected test names")
	}
	seen := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		if name == "" || strings.ContainsAny(name, "\x00\r\n") {
			return errors.New("JUnit expected test names are invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("JUnit expected test names are not unique")
		}
		seen[name] = struct{}{}
	}
	for _, name := range actual {
		if _, present := seen[name]; !present {
			return errors.New("JUnit selection does not match exact expected test names")
		}
	}
	return nil
}

func normalizeNativeJUnitXML(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("original JUnit report is missing or empty")
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return append([]byte(nil), data[3:]...), nil
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		return decodeNativeJUnitUTF16(data[2:], binary.LittleEndian)
	}
	if bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return decodeNativeJUnitUTF16(data[2:], binary.BigEndian)
	}
	// XML 1.0 permits UTF-16 without a BOM when the declaration identifies
	// it. These byte-order marks are unambiguous for a document beginning with
	// '<'; support both forms without adding a third-party charset package.
	if len(data) >= 2 && data[0] == '<' && data[1] == 0 {
		return decodeNativeJUnitUTF16(data, binary.LittleEndian)
	}
	if len(data) >= 2 && data[0] == 0 && data[1] == '<' {
		return decodeNativeJUnitUTF16(data, binary.BigEndian)
	}
	return append([]byte(nil), data...), nil
}

func decodeNativeJUnitUTF16(data []byte, order binary.ByteOrder) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, errors.New("malformed or unsafe JUnit report: UTF-16 has an odd byte length")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = order.Uint16(data[index*2 : index*2+2])
	}
	if len(units) > 0 && units[0] == 0xfeff {
		units = units[1:]
	}
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+1 >= len(units) || units[index+1] < 0xdc00 || units[index+1] > 0xdfff {
				return nil, errors.New("malformed or unsafe JUnit report: invalid UTF-16 surrogate pair")
			}
			index++
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			return nil, errors.New("malformed or unsafe JUnit report: invalid UTF-16 surrogate pair")
		}
	}
	return []byte(string(utf16.Decode(units))), nil
}

func nativeJUnitCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "utf-8", "utf8", "utf-16", "utf16", "utf-16le", "utf-16be":
		// UTF-16 is normalized before the decoder is created. Returning the
		// reader preserves the XML declaration while feeding UTF-8 bytes to
		// encoding/xml.
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported XML encoding %q", strconv.Quote(charset))
	}
}
