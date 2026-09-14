package repository

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// This file ports Test-BFNativeJUnit from Task.Runtime.ps1: the fresh,
// exactly-selected native JUnit report gate with aggregate cross-checks. It is
// a read-only binding shared by the controller and the native stage host.

var native1CAggregatePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

type native1CJUnitNode struct {
	local  string
	attrs  map[string]string
	nodes  []*native1CJUnitNode
}

// Native1CJUnit mirrors Test-BFNativeJUnit.
func Native1CJUnit(path string, expected []string, started, finished time.Time) (map[string]any, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, blocked("original native JUnit report missing or empty.")
	}
	if info.ModTime().UTC().Before(started.UTC()) || info.ModTime().UTC().After(finished.UTC().Add(2*time.Second)) {
		return nil, blocked("native JUnit is not fresh for this attempt.")
	}
	data, err := ReadFileBytes(path)
	if err != nil {
		return nil, blocked("original native JUnit report missing or empty.")
	}
	root, err := native1CJUnitTree(data)
	if err != nil {
		return nil, blocked("%v", err)
	}
	// PowerShell resolves $xml.DocumentElement.Name through the XmlElement
	// adapter: a present name attribute shadows the CLR element name, so the
	// root gate compares the attribute when it exists.
	rootName := root.local
	if nameAttribute, present := root.attrs["name"]; present {
		rootName = nameAttribute
	}
	if rootName != "testsuite" && rootName != "testsuites" {
		return nil, blocked("unsupported native JUnit root.")
	}
	cases := native1CJUnitCollect(root, "testcase")
	ids := make([]string, 0, len(cases))
	for _, testcase := range cases {
		ids = append(ids, testcase.attrs["classname"]+"."+testcase.attrs["name"])
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[strings.ToLower(id)] = true
	}
	joinedIDs := joinNative1CSorted(ids)
	joinedExpected := joinNative1CSorted(expected)
	if len(cases) == 0 || len(seen) != len(ids) || joinedIDs != joinedExpected {
		return nil, blocked("native JUnit selection differs from exact expected tests.")
	}
	if len(native1CJUnitCollect(root, "skipped")) > 0 {
		return nil, blocked("required native tests were skipped.")
	}
	failed := len(native1CJUnitCollect(root, "failure")) > 0 || len(native1CJUnitCollect(root, "error")) > 0
	suites := native1CJUnitSuites(root)
	for _, suite := range suites {
		for _, name := range []string{"tests", "failures", "errors", "skipped", "disabled"} {
			declared, present := suite.attrs[name]
			if !present {
				continue
			}
			if !native1CAggregatePattern.MatchString(declared) {
				return nil, blocked("invalid native JUnit aggregate.")
			}
			actual := 0
			switch name {
			case "tests":
				actual = len(native1CJUnitCollect(suite, "testcase"))
			case "failures":
				actual = len(native1CJUnitCollect(suite, "testcaseWithFailure"))
			case "errors":
				actual = len(native1CJUnitCollect(suite, "testcaseWithError"))
			}
			if fmt.Sprintf("%d", actual) != declared {
				return nil, blocked("inconsistent native JUnit aggregate.")
			}
		}
	}
	if failed {
		return nil, &KindError{Kind: "BF_FAIL", Message: "required native integration tests failed."}
	}
	return map[string]any{
		"tests":   toAnySlice(ids),
		"sha256":  fileSHA256(data),
		"outcome": "PASS",
	}, nil
}

// native1CJUnitTree parses the report without DTD or entity processing
// (encoding/xml never resolves them), mirroring the legacy safe reader.
func native1CJUnitTree(data []byte) (*native1CJUnitNode, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []*native1CJUnitNode
	var root *native1CJUnitNode
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("native JUnit report is malformed: %v", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			node := &native1CJUnitNode{local: typed.Name.Local, attrs: map[string]string{}}
			for _, attr := range typed.Attr {
				if attr.Name.Space == "" {
					node.attrs[attr.Name.Local] = attr.Value
				}
			}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.nodes = append(parent.nodes, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("native JUnit report has no root element")
	}
	return root, nil
}

func native1CJUnitCollect(node *native1CJUnitNode, selector string) []*native1CJUnitNode {
	result := []*native1CJUnitNode{}
	var walk func(*native1CJUnitNode)
	walk = func(current *native1CJUnitNode) {
		for _, child := range current.nodes {
			switch selector {
			case "testcaseWithFailure":
				if child.local == "testcase" && native1CJUnitHas(child, "failure") {
					result = append(result, child)
				}
			case "testcaseWithError":
				if child.local == "testcase" && native1CJUnitHas(child, "error") {
					result = append(result, child)
				}
			default:
				if child.local == selector {
					result = append(result, child)
				}
			}
			walk(child)
		}
	}
	walk(node)
	return result
}

func native1CJUnitHas(node *native1CJUnitNode, local string) bool {
	for _, child := range node.nodes {
		if child.local == local {
			return true
		}
	}
	return false
}

// native1CJUnitSuites collects every testsuite/testsuites element including
// the root and nested suites, exactly like //testsuite|//testsuites.
func native1CJUnitSuites(root *native1CJUnitNode) []*native1CJUnitNode {
	suites := []*native1CJUnitNode{}
	var walk func(*native1CJUnitNode)
	walk = func(current *native1CJUnitNode) {
		if current.local == "testsuite" || current.local == "testsuites" {
			suites = append(suites, current)
		}
		for _, child := range current.nodes {
			walk(child)
		}
	}
	walk(root)
	return suites
}

// joinNative1CSorted mirrors the legacy comparison: a case-insensitive
// (deterministic) sort followed by a case-sensitive joined equality.
func joinNative1CSorted(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := strings.ToLower(sorted[i]), strings.ToLower(sorted[j])
		if left != right {
			return left < right
		}
		return sorted[i] < sorted[j]
	})
	return strings.Join(sorted, "\n")
}
