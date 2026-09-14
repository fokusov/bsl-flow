package memoryhost

import (
	"encoding/json"
	"testing"
)

// The canonical vectors were captured from the real PowerShell
// Get-BFCanonicalJson / Get-BFHash in Task.Storage.ps1.
func TestCanonicalGoldens(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		canonical string
		hash      string
	}{
		{
			name:      "flat",
			value:     map[string]any{"schema_version": 1, "operation": "bind", "stage": "implement"},
			canonical: `{"operation":"bind","schema_version":1,"stage":"implement"}`,
			hash:      "48f33af22a43b379221c4ff6d54a8909817df210fd88043209c45772b768671f",
		},
		{
			name:      "nested",
			value:     map[string]any{"z": 26, "a": 1, "M": map[string]any{"nested": true, "arr": []any{1, 2, 3}, "n": nil, "s": "x"}},
			canonical: `{"M":{"arr":[1,2,3],"n":null,"nested":true,"s":"x"},"a":1,"z":26}`,
			hash:      "2d9daa6e9844d0080c26df799210c8b04a6a6e6dc58e497b3c6ad22a3e76d2fd",
		},
		{
			name:      "fingerprints",
			value:     map[string]any{"policy": "", "controller": "", "version": "", "schema": "", "toolchain": "unbound"},
			canonical: `{"controller":"","policy":"","schema":"","toolchain":"unbound","version":""}`,
			hash:      "a2d5e37ba9cb7e3fb39da7e617a382d809461392d3dec62812edcbbfb46749de",
		},
		{
			name:      "empty array",
			value:     []any{},
			canonical: `[]`,
			hash:      "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
		},
		{
			name:      "integers",
			value:     1,
			canonical: `1`,
			hash:      "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b",
		},
		{
			name:      "negative int64",
			value:     int64(-5),
			canonical: `-5`,
			hash:      "37aa1ccf80e481832b2db282d4d4f895ee1e31219b7d0f6aee8dc8968828341b",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			text, err := canonicalText(test.value)
			if err != nil {
				t.Fatalf("canonical: %v", err)
			}
			if text != test.canonical {
				t.Fatalf("canonical mismatch:\n got %s\nwant %s", text, test.canonical)
			}
			hash, err := hashValue(test.value)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if hash != test.hash {
				t.Fatalf("hash mismatch: got %s want %s", hash, test.hash)
			}
		})
	}
}

func TestCanonicalStringEscapes(t *testing.T) {
	// Captured from PowerShell: quote, backslash, tab, newline, carriage
	// return, a control byte, an astral pair and an accented letter.
	value := "q\"\\s\t\n\r\x01 \U0001F600é"
	hash, err := hashValue(value)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	const expected = "9cfe68800c400aa050659809f0e750c4ba8e0dfb672de89bb3bab92a82113eb4"
	if hash != expected {
		t.Fatalf("string hash mismatch: got %s want %s", hash, expected)
	}
	text, err := canonicalText("q\"s\t\n\r\x01")
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want := `"q\"s\t\n\r\u0001"`
	if text != want {
		t.Fatalf("escape mismatch: got %s want %s", text, want)
	}
}

func TestCanonicalNumberPassthrough(t *testing.T) {
	// Wire numbers decoded with UseNumber keep their literal form, matching
	// the Go controller's canonical encoding of the same document.
	value := map[string]any{"n": json.Number("1"), "big": json.Number("9007199254740993")}
	text, err := canonicalText(value)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want := `{"big":9007199254740993,"n":1}`
	if text != want {
		t.Fatalf("passthrough mismatch: got %s want %s", text, want)
	}
	if err := validCanonicalNumber("1e5"); err != nil {
		t.Fatalf("exponent literal rejected: %v", err)
	}
	if err := validCanonicalNumber("01"); err == nil {
		t.Fatal("leading zero accepted")
	}
}

func TestCanonicalDepthLimit(t *testing.T) {
	deep := any(json.Number("1"))
	for i := 0; i < 105; i++ {
		deep = map[string]any{"v": deep}
	}
	if _, err := canonicalBytes(deep); err == nil {
		t.Fatal("deeply nested value accepted beyond the depth limit")
	} else if got := err.Error(); got != "BF_INVALID: JSON value exceeds the maximum nesting depth." {
		t.Fatalf("depth error mismatch: %s", got)
	}
}
