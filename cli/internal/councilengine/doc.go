// Package councilengine is the native Go port of the PowerShell council
// engine family (1c-spec-review/scripts/Council.*.ps1,
// Invoke-CouncilReview.ps1): the live spec-review council cycle — snapshot,
// role views, attempts, budget ledger, member/chair dispatch, v2 review
// assembly, prepared publication and resume. The HTTP transport itself lives
// in cli/internal/counciltransport; this package owns everything around it.
//
// Byte parity: every persisted artifact (attempt/result/budget/publication
// files, review.json v2) and every hash input (binding, payload, aggregate,
// digest) is produced through the psjson replicator of PowerShell 7
// ConvertTo-Json documented in psjson.go; divergences are never silent.
package councilengine
