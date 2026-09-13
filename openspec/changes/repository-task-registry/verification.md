# Verification: repository-task-registry

Date: 2026-09-12. Scope: native planned/read-only registry slice; activation/adoption remain staged BLOCKED as specified.

Independent code review: accepted after remediation. `go test ./... -count=1` and `go vet ./...` passed using an isolated local Go cache. Durable logs are under `work/spec-completion-20260912/registry-full-go-localcache.log` and `registry-go-vet.log`.

The tests exercise the real command dispatcher and journals: canonical/legacy conflicts and corrupt siblings fail closed; read commands do not create repository identity; Git environment cannot redirect repository selection; removed origin worktrees become stale; existing evidence files remain fresh. Projected timestamps are validated as RFC3339Nano without echoing rejected input, and ordering compares instants. Credential-like values and PEM bodies do not leak into public JSON. Default list pagination is 100 while overview uses the complete set.

Final embedded CLI build and reproducibility passed; the exact executable passed 28 smoke checks. The first long-TEMP smoke failed at the Git path-length limit and was retained; the same frozen script and binary passed from the normal short package root. This does not establish Windows power-loss durability or authorize native controller write operations. All mandatory offline checks completed, with the package tail resumed after the same Git path-length limitation. The final combined acceptance is recorded in `docs/THREE_SPEC_REMEDIATION_2026-09-12_RU.md`.