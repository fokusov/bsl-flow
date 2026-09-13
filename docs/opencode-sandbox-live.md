# OpenCode sandbox live spike

This is a bounded live capability check after the elevated native Codex filesystem probe passed. It is not a managed adapter, benchmark result, or a security claim for provider networking, MCP, or external tools.

The runner creates one disposable directory per call. Its OpenCode profile and all XDG state are isolated there. Project configuration, Claude compatibility, and external skills are disabled. Native Codex launches OpenCode with an elevated profile that permits only source and private scratch writes, config reads, and provider networking; two synthetic siblings are explicitly denied. The standard DeepSeek auth record is read by the host runner and passed only via the child environment, never through command-line arguments, generated configuration, raw evidence, or the report.

The first run permits no tools. The optional second run permits only OpenCode read/edit tools and asks for one deterministic source-local read/edit action. Raw JSONL and stderr stay in the ignored probe directory. A later controller must parse every event shape and preserve unknown or incomplete sequences as blocked evidence.

## 2026-09-10 attempt result

The strict no-tools attempt reached the native elevated sandbox but stopped before an OpenCode turn, provider request, or model usage. OpenCode attempted to create `config/opencode/.gitignore`; the configured profile correctly exposed that directory as read-only, so the sandbox returned `FileSystem.writeFile` with an access error. The raw stderr and receipt are retained in `work/benchmark-integration/opencode-sandbox-live/run-2-no-tools-elevated`.

This is a concrete incompatibility between the requested read-only configuration root and OpenCode 1.18.30 startup behavior. The controller can preseed the otherwise empty `.gitignore` as part of the disposable config fixture, keeping that root read-only to OpenCode. This is not a permission relaxation.

With that preseeded fixture, one no-tools call and one controlled source-local tool call succeeded. OpenCode reported the requested `deepseek/deepseek-v4-flash` model, but the stream has no independent backend model attestation. The no-tools run emitted `step_start`, one `text`, and terminal `step_finish` with `reason=stop`; reported tokens were total 2055, input 347, output 25, reasoning 19, cache read 1664, cache write 0, and cost USD 0.0000655592.

The controlled call emitted three steps: completed `read` of `source/input.txt`, completed `write` of `source/edited.txt`, then the terminal JSON text. Each tool step finished with `reason=tool-calls`; the terminal one finished with `reason=stop`. OpenCode reported per-step costs USD 0.0002702392, 0.0000682584, and 0.0000471968, for USD 0.0003856944 in that call. The two calls total USD 0.0004512536 as reported by OpenCode. `edited.txt` contains the requested value and both synthetic protected sibling contents remained unchanged. This call did not attempt to read either protected sibling; their denied-read/write boundary remains supported by the separate parent/child sandbox probe, not by the model stream.

The raw streams and receipts are in `work/benchmark-integration/opencode-sandbox-live/run-3-no-tools-preseeded` and `run-4-local-edit`. They establish the observed JSONL contract for these event shapes only. They do not establish MCP/tool server isolation, protect network traffic, or make any claim about provider security.
