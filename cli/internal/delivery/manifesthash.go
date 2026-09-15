package delivery

// HashManifest returns the domain's canonical identity hash of a manifest.
// Callers that build an AcceptedSource outside this package need the same
// freshness anchor verifyAccepted recomputes, so the hash is exposed instead
// of every caller reimplementing the canonical shape.
func HashManifest(manifest Manifest) (string, error) {
	return hashValue(manifest.value())
}
