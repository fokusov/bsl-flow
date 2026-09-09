# Review reconciliation contract

Save `review-reconciliation.json` beside `review.json` after independently checking every finding.

```json
{
  "schema_version": 1,
  "review_sha256": "64 lowercase hex characters",
  "draft_spec_sha256": "hash copied from review.inputs.spec_sha256",
  "final_spec_sha256": "hash of the revised spec.md",
  "draft_design_sha256": null,
  "final_design_sha256": null,
  "reconciled_at_utc": "2026-09-01T12:00:00Z",
  "summary": "Minimal targeted revision summary.",
  "decisions": [
    {
      "finding_id": "R-001",
      "decision": "accepted",
      "reason": "Why the finding is correct.",
      "evidence": "Task or project evidence.",
      "status": "addressed",
      "resolution": "What changed without broad rewrite.",
      "spec_ref_after": "Требуемое поведение / 2"
    },
    {
      "finding_id": "R-002",
      "decision": "rejected",
      "reason": "Why the finding does not apply.",
      "evidence": "Concrete task or project evidence.",
      "status": "not_applicable",
      "resolution": "No specification change.",
      "spec_ref_after": "Контекст 1С"
    }
  ],
  "do_not_change_checks": [
    {
      "item": "The exact text from review.do_not_change",
      "decision": "preserved",
      "reason": "Why this part remains correct.",
      "evidence": "How preservation was checked."
    }
  ]
}
```

Rules:

- Include every review finding exactly once; decisions are only `accepted` or `rejected`.
- An accepted finding must be `addressed` with a concrete resolution and final spec reference.
- A rejected finding must be `not_applicable` with concrete evidence; disagreement alone is not evidence.
- Include every `do_not_change` item exactly once. Use `preserved` when it remains correct or `rejected` when it conflicts with a validated finding; either decision requires reason and evidence.
- Hashes bind the reconciliation to the reviewed draft and final specification.
- When `design.md` exists, use its reviewed and final SHA-256 values instead of `null`.
- Every property shown in the contract is required. Final validation rejects malformed legacy reconciliation files and writes a fresh failed `final-validation.json`; an older PASS is never retained as current evidence.
- If an unresolved blocker remains, do not claim final validation or begin implementation.
