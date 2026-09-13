package runner

import (
	"strings"
	"time"
)

// Ownership is the single-owner queue lease. The source runner holds one
// exclusive lock over the runner directory for the entire queue run
// (Enter-BFLock in Invoke-BFTaskQueue): a second distinct holder receives a
// conflict, never a takeover. ExpiresAt is zero for an unexpired exclusive
// hold; a non-zero expiry marks a supervised lease whose holder may have died.
type Ownership struct {
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

func (s *State) leaseEnd(now time.Time) time.Time {
	if s.LeaseTTL <= 0 {
		return time.Time{}
	}
	return now.Add(s.LeaseTTL)
}

// Acquire takes queue ownership for holder. With no current owner it always
// succeeds. The same holder may re-acquire (its lease is refreshed). A second
// distinct holder never takes over: while the lease is valid it gets a
// conflict, and once the lease has expired it is still refused and directed to
// RecoverOwnership, because an expired lease must be reconciled rather than
// silently stolen.
func (s *State) Acquire(holder string, now time.Time) (bool, error) {
	if strings.TrimSpace(holder) == "" {
		return false, invalid("runner ownership requires a holder identity")
	}
	if s.Owner == nil {
		s.Owner = &Ownership{Holder: holder, AcquiredAt: now, ExpiresAt: s.leaseEnd(now)}
		return true, nil
	}
	if s.Owner.Holder == holder {
		if !s.Owner.ExpiresAt.IsZero() && !now.Before(s.Owner.ExpiresAt) {
			// The holder's own lease lapsed; another copy with the same
			// identity may have run meanwhile.
			s.NeedsReconciliation = true
		}
		s.Owner.AcquiredAt = now
		s.Owner.ExpiresAt = s.leaseEnd(now)
		return true, nil
	}
	if s.Owner.ExpiresAt.IsZero() || now.Before(s.Owner.ExpiresAt) {
		return false, conflict("runner queue ownership is held by %s", s.Owner.Holder)
	}
	return false, conflict("runner queue ownership lease of %s expired at %s; recover it explicitly", s.Owner.Holder, s.Owner.ExpiresAt.Format(time.RFC3339))
}

// RecoverOwnership transfers ownership after a lease expiry. It is the only
// path away from a dead holder, it refuses live owners, and it always marks
// the state as needing reconciliation: the previous holder died mid-run, so
// the journal must be replayed and inspected before dispatch resumes.
func (s *State) RecoverOwnership(holder string, now time.Time) error {
	if strings.TrimSpace(holder) == "" {
		return invalid("runner ownership requires a holder identity")
	}
	if s.Owner == nil || s.Owner.Holder == holder {
		// Nothing (or already this holder) to recover from.
		s.Owner = &Ownership{Holder: holder, AcquiredAt: now, ExpiresAt: s.leaseEnd(now)}
		return nil
	}
	if s.Owner.ExpiresAt.IsZero() {
		return conflict("runner queue ownership of %s does not expire; it must be released", s.Owner.Holder)
	}
	if now.Before(s.Owner.ExpiresAt) {
		return conflict("runner queue ownership lease of %s is still valid", s.Owner.Holder)
	}
	s.Owner = &Ownership{Holder: holder, AcquiredAt: now, ExpiresAt: s.leaseEnd(now)}
	s.NeedsReconciliation = true
	return nil
}

// Release drops ownership. The source runner always disposes its lock in a
// finally block when the queue run ends.
func (s *State) Release(holder string) error {
	if s.Owner == nil {
		return invalid("runner queue ownership is not held")
	}
	if s.Owner.Holder != holder {
		return conflict("runner queue ownership is held by %s", s.Owner.Holder)
	}
	s.Owner = nil
	return nil
}

// MarkReconciled clears NeedsReconciliation after the supervisor has replayed
// a complete journal or an operator has inspected the interrupted state.
func (s *State) MarkReconciled() {
	s.NeedsReconciliation = false
}
