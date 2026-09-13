package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSealInputCanonicalBytesAndHash(t *testing.T) {
	data, digest := SealInput("implement stage", `C:\DEV\BSL Flow\work\task`)
	expected := `{"prompt":"implement stage","worktree":"C:/DEV/BSL Flow/work/task"}`
	if string(data) != expected {
		t.Fatalf("sealed input mismatch:\n got %s\nwant %s", data, expected)
	}
	sum := sha256.Sum256(data)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("sealed digest is not the SHA-256 of the canonical bytes: %s", digest)
	}
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		t.Fatalf("sealed digest must be lowercase hex: %s", digest)
	}
}

func TestSealInputDeterminismAndSeparation(t *testing.T) {
	firstData, firstDigest := SealInput("same prompt", `C:\work\one`)
	secondData, secondDigest := SealInput("same prompt", `C:\work\one`)
	if string(firstData) != string(secondData) || firstDigest != secondDigest {
		t.Fatal("sealed input must be deterministic for identical inputs")
	}
	_, otherDigest := SealInput("same prompt", `C:\work\two`)
	if otherDigest == firstDigest {
		t.Fatal("different worktrees must produce different sealed inputs")
	}
	// Secrets are excluded by construction: the canonical bytes carry only the
	// prompt and worktree members.
	for _, secret := range []string{"token", "password", "api_key"} {
		if strings.Contains(string(firstData), secret) {
			t.Fatalf("sealed input leaked %q", secret)
		}
	}
}
