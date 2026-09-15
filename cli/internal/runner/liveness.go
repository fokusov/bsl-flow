package runner

// ProcessIdentity is one durable process ownership marker (start.json
// controller_process or an attempt process.json): the pid plus its exact UTC
// start time, the identity Get-BFOwnedProcess compares against.
type ProcessIdentity struct {
	PID          int64
	StartTimeUTC string
}

// Liveness probes whether a stored process identity still names a live
// process. Callers inject fakes for tests; OSLiveness is the default probe.
type Liveness interface {
	Alive(identity ProcessIdentity) bool
}

// OSLiveness probes the live process table. An identity that cannot be
// resolved to a live process reports not alive, exactly like
// Get-BFOwnedProcess returning null.
type OSLiveness struct{}

func (OSLiveness) Alive(identity ProcessIdentity) bool {
	return osProcessAlive(identity.PID, identity.StartTimeUTC)
}
