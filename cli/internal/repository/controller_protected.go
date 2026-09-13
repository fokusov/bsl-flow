package repository

import "strings"

// The controller checks the same protected inputs before and after every
// implementation, including a correction or repair supplied by the provider.
func validateNativeProtectedTests(payload, before, after map[string]any) error {
	for _, raw := range anyItems(asMap(payload["request"])["criteria"]) {
		for _, value := range anyItems(asMap(raw)["protected_paths"]) {
			scope := strings.TrimSuffix(strings.ReplaceAll(asStringOr(value), `\`, "/"), "/")
			selectFiles := func(manifest map[string]any) (map[string]any, bool) {
				files := map[string]any{}
				present := false
				for _, item := range anyItems(manifest["files"]) {
					file := asMap(item)
					path := asStringOr(file["path"])
					if scope == "." || strings.EqualFold(path, scope) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(scope)+"/") {
						files[path] = file
						present = present || !asBoolOr(file["deleted"])
					}
				}
				return files, present
			}
			oldFiles, oldPresent := selectFiles(before)
			newFiles, newPresent := selectFiles(after)
			if !oldPresent || !newPresent || !equalJSON(oldFiles, newFiles) {
				return blocked("implementation changed or lacks protected test inputs: %s", scope)
			}
		}
	}
	return nil
}
