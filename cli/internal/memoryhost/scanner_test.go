package memoryhost

import "testing"

// The scanner diagnostics are ported verbatim from Test-BFJsonSyntax; the
// messages below are the exact bytes surfaced inside memory blocker text.
func TestJSONSyntaxKinds(t *testing.T) {
	cases := []struct {
		text string
		kind string
	}{
		{`{"a":1}`, "object"},
		{` [1,2]`, "array"},
		{`null`, "scalar"},
		{`  "x"`, "scalar"},
		{`{}`, "object"},
		{`[]`, "array"},
		{`{"a":{"b":[true,false,null,-1.5e+3]}}`, "object"},
	}
	for _, test := range cases {
		kind, err := testJSONSyntax(test.text)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", test.text, err)
		}
		if kind != test.kind {
			t.Fatalf("%s: kind %s want %s", test.text, kind, test.kind)
		}
	}
}

func TestJSONSyntaxErrors(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{``, "BF_INVALID: Expected a JSON value."},
		{`   `, "BF_INVALID: Expected a JSON value."},
		{`{"a":1`, "BF_INVALID: Expected comma or closing brace in JSON object."},
		{`[1,`, "BF_INVALID: Expected a JSON value."},
		{`[1 2]`, "BF_INVALID: Expected comma or closing bracket in JSON array."},
		{`{"a" 1}`, "BF_INVALID: Expected a colon after JSON object key."},
		{`{"a":1,"a":2}`, "BF_INVALID: Duplicate JSON object key: a."},
		{`{"a":1,"A":2}`, "BF_INVALID: Duplicate JSON object key: A."},
		{`{"a":1} x`, "BF_INVALID: Unexpected data after JSON value."},
		{`{'a':1}`, "BF_INVALID: Expected a JSON string."},
		{`{"a":unu}`, "BF_INVALID: Invalid JSON value."},
		{`{"a":"un`, "BF_INVALID: Unterminated JSON string."},
		{`{"a":"x` + "\x01" + `"}`, "BF_INVALID: Unescaped control character in JSON string."},
		{`{"a":"\q"}`, "BF_INVALID: Invalid JSON escape."},
		{`{"a":"\u12"}`, "BF_INVALID: Invalid JSON Unicode escape."},
		{`{"a":"\u12`, "BF_INVALID: Incomplete JSON Unicode escape."},
		{`{"a":"x\`, "BF_INVALID: Incomplete JSON escape."},
		{`tru`, "BF_INVALID: Invalid JSON value."},
	}
	for _, test := range cases {
		if _, err := testJSONSyntax(test.text); err == nil {
			t.Fatalf("%q: expected error", test.text)
		} else if err.Error() != test.want {
			t.Fatalf("%q: got %q want %q", test.text, err.Error(), test.want)
		}
	}
}
