# Owner override: TEMPORARY, pending re-review

This is a **temporary** governance override, not a closed decision. It records why
`execution-contract-v01` was merged while its council review was BLOCKED (see
`review-blocked.md`), and what re-opens the question.

owner: Igor Fokusov
date: 2026-09-26
reason: >
  The owner approved the remediation plan (docs/plans/2026-09-26-remediation-plan.md) on
  2026-09-26. That plan's own review of this change (§0, item 8) found that the council
  BLOCK was caused by the chair inventing a non-existent requirement anchor
  ("Форматы файлов / Evidence/T-NNN.json") on two separate runs, rather than by a real defect
  in the specification or the implementation. The implementation is independently covered by
  deterministic, offline test suites: Test-SpecContractLint (100 checks) and
  Test-ExecutionGraphDiscipline (37 checks), both registered in Test-BSLFlowPackage. Plan item
  Ф1.6 fixes the root cause going forward by making the chair's requirement references an
  enum generated from `anchors[].id` at review time, so a model can no longer reference an
  anchor that does not exist in the specification.
accepted_risks:
  no_passing_independent_review: >
    The specification has no passing independent (council) review. The only review runs to
    date both ended BLOCKED at the deterministic chair gate, not PASS or PASS_WITH_LIMITATIONS.
  reopen_condition: >
    This override lapses once execution-contract-v01 is re-reviewed by a heterogeneous council
    (distinct models across chair and critics, not the current single-model
    multi_role_single_model setup) after Ф1.6 ships. Re-review is tracked as a follow-up; if
    that re-review also ends BLOCKED for a reason other than the anchor defect, this override
    must be revisited by the owner, not silently renewed.
