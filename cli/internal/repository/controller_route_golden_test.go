package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// The oracle was exported by executing the frozen legacy routing and
// classification functions. Its source hashes travel with the fixture so a
// change to native routing cannot silently redefine the expected behavior.
func TestNativeControllerLegacyGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "legacy-route-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	routes, ok := golden["routes"].([]any)
	if !ok || len(routes) != 162 {
		t.Fatal("legacy route oracle must contain all 162 cases")
	}
	classifications, ok := golden["classifications"].([]any)
	if !ok || len(classifications) != 54 {
		t.Fatal("legacy classification oracle must contain all 54 cases")
	}
	for _, raw := range routes {
		item := raw.(map[string]any)
		t.Run("route/"+item["name"].(string), func(t *testing.T) {
			got := routeForController(item["controller"].(map[string]any))
			if !equalJSON(got, item["route"]) {
				t.Fatalf("native route = %v; frozen legacy route = %v", got, item["route"])
			}
		})
	}
	for _, raw := range classifications {
		item := raw.(map[string]any)
		t.Run("classification/"+item["name"].(string), func(t *testing.T) {
			before, err := cloneObject(item["before"].(map[string]any))
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"classification": before}
			if err := strengthenClassification(payload, item["proposal"].(map[string]any)); err != nil {
				t.Fatal(err)
			}
			if !equalJSON(payload["classification"], item["after"]) {
				t.Fatalf("native classification = %#v; frozen legacy classification = %#v", payload["classification"], item["after"])
			}
		})
	}
}
