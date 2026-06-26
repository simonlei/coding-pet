# feat: Coding Pet Dashboard — Claude Code collection + full rename

**Date:** 2026-06-26
**Type:** feat
**Depth:** Standard
**Origin:** docs/brainstorms/2026-06-26-coding-pet-multi-tool-dashboard-requirements.md

---

## Summary

Extend the dashboard agent to collect **Claude Code** sessions alongside CodeBuddy, tag every session with its source tool, group sessions by tool in the UI, reuse the existing yellow flashing alert for Claude Code sessions that need user input/approval, and rename the product end-to-end from "CodeBuddy" to "Coding Pet" (UI strings, Go module path, binaries, Makefile, README).

The collection architecture already in place for CodeBuddy (scan pid files → derive state → report to server → poll on frontend) is reused. Claude Code's on-disk layout is structurally close to CodeBuddy's but differs in field names, freshness signal (`updatedAt`/`statusUpdatedAt`, no `lastHeartbeat`), and state source (a `status` field rather than JSONL `function_call` parsing).

---

## Problem Frame

Today the agent only collects CodeBuddy sessions (`~/.codebuddy/sessions/*.json` + JSONL logs). Developers also run Claude Code and have no unified view of which AI coding session — across both tools and all machines — currently needs their attention. The dashboard must show both tools, distinguish them, and flag any waiting session with the existing alert. The product name should reflect its broadened scope.

---

## Requirements

Traced from the origin requirements doc:

- **R1** — Agent collects Claude Code sessions from `$CLAUDE_CONFIG_DIR/sessions/*.json`, falling back to `~/.tclaude/sessions/*.json` when `CLAUDE_CONFIG_DIR` is unset. (origin §1)
- **R2** — Claude Code session state derived primarily from the pid-file `status` field, mapped onto the existing `SessionState` enum, including `waiting_for_input` / `waiting_for_approval`. (origin §2)
- **R3** — Waiting Claude Code sessions drive the **existing** yellow flashing alert; no new alert UI. (origin §2)
- **R4** — Claude Code liveness reuses the CodeBuddy model (process-alive + staleness), adapted to `updatedAt`/`statusUpdatedAt` in place of `lastHeartbeat`. (origin §3)
- **R5** — Each session is tagged with its source tool (`codebuddy` / `claude_code`); the dashboard **groups sessions by tool** within each machine. (origin §4)
- **R6** — Full rename CodeBuddy → Coding Pet: UI title/h1, Go module path, binaries + Makefile, README, and source/help strings — **except** the literal `~/.codebuddy/...` monitoring paths and the generic `DASHBOARD_*` env vars / `--server` flags, which are retained. (origin §5, refined in plan synthesis)

---

## Key Technical Decisions

- **KTD1 — `Tool` field on `SessionInfo`.** Add `Tool SessionTool` (`json:"tool"`) to `protocol.SessionInfo`, with constants `ToolCodeBuddy = "codebuddy"` and `ToolClaudeCode = "claude_code"`. The existing CodeBuddy collector sets `ToolCodeBuddy`; the new collector sets `ToolClaudeCode`. Defaulting an absent value to CodeBuddy keeps older agents backward-compatible with a newer server.

- **KTD2 — Collector source abstraction.** Generalize collection so the agent runs two sources and concatenates results, rather than hardcoding one. Each source knows its own pid-file directory, field shape, and state derivation. Keeps the CodeBuddy path untouched in behavior while letting Claude Code plug in beside it.

- **KTD3 — Claude Code state from `status` field, JSONL as documented fallback only.** The Claude Code pid file carries `status`. Map: `busy` → `active`; `idle` → `active` (idle = awaiting next user prompt, not blocked — no flash); any explicit waiting value → `waiting_for_input` / `waiting_for_approval`. **The exact waiting string(s) and whether input vs. approval is distinguishable must be confirmed against a live waiting session at implementation time** (see Assumptions A1). JSONL deep-parsing is the contingency, not built unless `status` proves insufficient.

- **KTD4 — Liveness via `statusUpdatedAt` + process-alive.** Claude Code has no `lastHeartbeat`. Use `statusUpdatedAt` (fallback `updatedAt`) for the staleness window, then `isProcessAlive(pid)` — mirroring `collectOne` in internal/collector/collector.go. Reuse the existing platform-specific `isProcessAlive`.

- **KTD5 — Config-dir resolution.** Resolve the Claude Code base dir as: `$CLAUDE_CONFIG_DIR` if set, else `~/.tclaude`. Sessions live in `<base>/sessions/*.json`. (Note: `~/.claude` is the vanilla default, but this deployment uses `~/.tclaude`; the env var is the authoritative override and the fallback matches the observed environment per origin A2.)

- **KTD6 — Grouping in the frontend, not the protocol.** The server keeps returning a flat `Sessions` list per machine (plus the new `Tool` field). The frontend groups by tool at render time. Avoids changing the `MachineStatus`/`DashboardResponse` shape beyond the per-session tag.

- **KTD7 — Rename scope.** Swap only the "CodeBuddy" brand token to "Coding Pet" / "coding-pet". Module `github.com/simonlei/codebuddy-dashboard` → `github.com/simonlei/coding-pet-dashboard`; binaries `dashboard-agent`/`dashboard-server` → `coding-pet-agent`/`coding-pet-server`. **Retain** `~/.codebuddy/` paths (they point at the monitored tool) and `DASHBOARD_*` env vars + `--server`/`--token`/`--id` flags (generic deployment contract, no brand reference).

---

## Implementation Units

### U1. Add `Tool` field and tool constants to the protocol

**Goal:** Every session carries its source tool; downstream code can group/filter by it.
**Requirements:** R5
**Dependencies:** none
**Files:**
- `internal/protocol/types.go` (modify)
- `internal/protocol/types_test.go` (create)

**Approach:** Add `SessionTool` string type with `ToolCodeBuddy`/`ToolClaudeCode` constants. Add `Tool SessionTool` json:"tool" to `SessionInfo`. No server-side aggregation change needed yet.

**Patterns to follow:** Mirror the existing `SessionKind` type+const block in internal/protocol/types.go:14-21.

**Test scenarios:**
- A `SessionInfo` with `Tool: ToolClaudeCode` round-trips through JSON marshal/unmarshal with `"tool":"claude_code"`.
- A JSON payload omitting `tool` unmarshals to an empty `Tool` (documents the backward-compat default that U4 normalizes).

---

### U2. Tag existing CodeBuddy collection with `ToolCodeBuddy`

**Goal:** The current collector stamps every session it produces as CodeBuddy.
**Requirements:** R5
**Dependencies:** U1
**Files:**
- `internal/collector/collector.go` (modify)
- `internal/collector/collector_test.go` (create)

**Approach:** Set `Tool: protocol.ToolCodeBuddy` in the `SessionInfo` returned by `collectOne` (internal/collector/collector.go:75). No behavioral change otherwise.

**Patterns to follow:** Existing `collectOne` construction at internal/collector/collector.go:75-85.

**Test scenarios:**
- `Covers AE.` A collected CodeBuddy session has `Tool == ToolCodeBuddy`.
- Existing state-derivation behavior (active/waiting/terminated) is unchanged by the added field — assert one representative state still resolves correctly.

---

### U3. Claude Code pid-file reader + state mapping (TDD)

**Goal:** Read Claude Code pid files and derive a `SessionInfo` with correct state, tool, and liveness.
**Requirements:** R1, R2, R4
**Dependencies:** U1
**Files:**
- `internal/collector/claudecode.go` (create)
- `internal/collector/claudecode_test.go` (create)

**Approach:**
- `claudeConfigDir()` → `$CLAUDE_CONFIG_DIR` or `~/.tclaude` (KTD5).
- `readClaudeCodePIDFiles()` → glob `<base>/sessions/*.json`, parse into a CC-specific struct (fields: `pid`, `sessionId`, `cwd`, `startedAt`, `kind`, `entrypoint`, `status`, `version`, `updatedAt`, `statusUpdatedAt`). Skip unparseable files, mirroring `ReadPIDFiles` in internal/collector/pidfile.go:26-50.
- `mapClaudeStatus(status string) SessionState` (KTD3): `busy`/`idle` → active; explicit waiting value(s) → waiting_for_input/approval; unrecognized → active (busy-equivalent) so a working session is never misflagged.
- State resolution per session: stale (`now - statusUpdatedAt > 60s`) or `!isProcessAlive(pid)` → terminated; else `mapClaudeStatus(status)`. `LastActivity` = `statusUpdatedAt` (fallback `updatedAt`). `Tool` = `ToolClaudeCode`.

**Execution note:** Implement test-first — write the table-driven `mapClaudeStatus` and state-resolution tests from fixtures before the implementation. Repo has no tests yet; global CLAUDE.md mandates TDD.

**Patterns to follow:** `ReadPIDFiles` (internal/collector/pidfile.go), `collectOne` liveness ladder (internal/collector/collector.go:39-65), reuse `isProcessAlive` (internal/collector/pidfile_unix.go).

**Test scenarios:**
- `mapClaudeStatus("busy")` → active; `("idle")` → active; explicit waiting value → waiting_for_input / waiting_for_approval (table-driven; the exact strings filled in per A1).
- `mapClaudeStatus("")` and an unknown value → active (never spuriously waiting).
- A pid file with `statusUpdatedAt` older than the staleness window → terminated even if `status` is `busy`.
- A pid file whose `pid` is not alive → terminated (use a pid guaranteed dead, e.g. a never-allocated high pid, behind the existing `isProcessAlive`).
- A live `busy` session within the window → active with `Tool == ToolClaudeCode` and `LastActivity == statusUpdatedAt`.
- Malformed JSON file in the sessions dir is skipped, not fatal.
- `CLAUDE_CONFIG_DIR` set vs unset selects the right base dir (use `t.Setenv`).

---

### U4. Wire Claude Code source into collection + server-side default

**Goal:** The agent collects from both tools each cycle; the server treats untagged sessions as CodeBuddy.
**Requirements:** R1, R5
**Dependencies:** U2, U3
**Files:**
- `internal/collector/collector.go` (modify)
- `internal/server/store.go` (modify)
- `internal/server/store_test.go` (create)

**Approach:**
- In `CollectSessions` (internal/collector/collector.go:21), after collecting CodeBuddy sessions, append Claude Code sessions from U3. Both already carry their `Tool`.
- In `Store.UpdateMachine` (internal/server/store.go:32), normalize any session with empty `Tool` to `ToolCodeBuddy` (backward-compat per KTD1) so the frontend always sees a tool value.
- Confirm `GetDashboard` counts (active/waiting/approval) work unchanged across the combined set — they key on `State`, which is tool-agnostic.

**Patterns to follow:** Existing append loop in `CollectSessions`; existing field-copy in `UpdateMachine`.

**Test scenarios:**
- A report with mixed `codebuddy` and `claude_code` sessions stores both; counts aggregate across tools (one waiting CC session increments `WaitingCount`).
- A session arriving with empty `Tool` is normalized to `ToolCodeBuddy` after `UpdateMachine`.
- Offline override (`CheckOffline` setting state→unknown) still applies regardless of tool.

---

### U5. Group sessions by tool in the frontend

**Goal:** Within each machine, sessions render under a per-tool subheading; the waiting alert section is unchanged in behavior.
**Requirements:** R5, R3
**Dependencies:** U4
**Files:**
- `internal/server/index.html` (modify)

**Approach:**
- In `renderMachineList` (index.html:632), before emitting session cards, partition `machine.sessions` by `session.tool` and emit a small tool-group label (e.g. "CodeBuddy" / "Claude Code") before each group's cards. Preserve the existing `terminated` filter and collapse/expand behavior.
- The top waiting section (`renderWaitingSection`, index.html:593) already keys on `state` only — it continues to surface both tools' waiting/approval sessions with the existing yellow `waiting` / `waiting_for_approval` styling (R3). Optionally include the tool name in the waiting card's machine prefix line for clarity.
- Empty tool groups render nothing (no header for a tool with zero non-terminated sessions).

**Patterns to follow:** Existing `.section-title` styling (index.html:82) for the group label; existing `renderSessionCard` (index.html:722) unchanged.

**Test scenarios:** Test expectation: none (no JS test harness in repo) — verified manually per Verification below.

---

### U6. Full rename CodeBuddy → Coding Pet

**Goal:** No "CodeBuddy" product branding, `codebuddy-dashboard` module path, or `dashboard-agent`/`dashboard-server` binary names remain; monitoring paths and `DASHBOARD_*` contract retained.
**Requirements:** R6
**Dependencies:** U1–U5 (do last to avoid churn during feature work)
**Files:**
- `go.mod` (modify — module path)
- all `*.go` files importing `github.com/simonlei/codebuddy-dashboard/...` (modify import paths)
- `Makefile` (modify — output binary names + targets)
- `internal/server/index.html` (modify — `<title>` line 6, `<h1>` line 445)
- `cmd/server/main.go`, `cmd/agent/main.go` (modify — log lines "dashboard-server"/"dashboard-agent" → coding-pet equivalents)
- `README.md` (modify — title, multi-tool description, build artifact names)

**Approach:**
- Module: `github.com/simonlei/codebuddy-dashboard` → `github.com/simonlei/coding-pet-dashboard`; update every import (KTD7).
- Binaries/Makefile: `dashboard-agent`→`coding-pet-agent`, `dashboard-server`→`coding-pet-server`.
- UI: title + h1 → "Coding Pet Dashboard".
- README: retitle to "Coding Pet Dashboard", describe CodeBuddy **and** Claude Code monitoring, update binary names.
- **Do NOT touch:** `~/.codebuddy/...` path literals in internal/collector/* (they address the monitored CodeBuddy tool); `DASHBOARD_SERVER`/`DASHBOARD_TOKEN`/`DASHBOARD_ID` env vars and `--server`/`--token`/`--id` flags (generic, no brand reference, renaming breaks deployments).

**Execution note:** Mechanical sweep — run after features land. Grep `CodeBuddy`, `codebuddy-dashboard`, `dashboard-agent`, `dashboard-server` and review each hit against the retain-list before changing.

**Test scenarios:** Test expectation: none (rename) — verified by clean build + grep audit (Verification below). The existing collector/protocol tests from U1–U4 must still pass after the import-path rewrite.

---

## Verification

- `make build` succeeds and produces `coding-pet-agent` / `coding-pet-server`.
- `go test ./...` passes (U1–U4 tests).
- Grep audit: no `CodeBuddy` / `codebuddy-dashboard` / `dashboard-agent` / `dashboard-server` outside the documented retain-list (`~/.codebuddy/` paths).
- Manual: with a CodeBuddy session and a Claude Code session running, the dashboard shows both, grouped by tool per machine, with the renamed title.
- Manual: a Claude Code session blocked on a permission/choice prompt flashes yellow in the waiting section, identical to CodeBuddy; an `idle`/terminated CC session does not flash.

---

## Scope Boundaries

**In scope:** Claude Code pid-file collection, `status`→state mapping reusing the existing alert, per-session tool tag, tool-grouped rendering, full Coding Pet rename (brand token only).

**Outside this product's identity (from origin):**
- Monitoring any third coding tool beyond CodeBuddy and Claude Code.
- Changes to the alert visual itself (color/animation) — reused as-is.

### Deferred to Follow-Up Work
- Claude Code JSONL deep-parsing (its `message.content[]` / `stop_reason` schema) — only if A1 reveals `status` cannot distinguish input vs. approval.
- Windows-specific validation of `isProcessAlive` for Claude Code pids (reuses the existing cross-platform implementation; no new code, but untested on Windows here).

---

## Assumptions / Dependencies

- **A1 (must verify at implementation time):** The Claude Code pid-file `status` field emits explicit waiting value(s) when a session is blocked on a prompt. Observed live values so far are only `busy`/`idle`; the user confirmed a waiting value exists. Implementation must capture a live waiting session to learn the exact string(s) and whether input vs. approval is distinguishable. **Contingency:** if `status` alone cannot distinguish them, fall back to JSONL last-entry parsing for Claude Code — only the input-vs-approval split is at risk; the alert still fires on any waiting value. (origin A1)
- **A2:** `CLAUDE_CONFIG_DIR` override + `~/.tclaude` fallback is the correct discovery path (confirmed live on this machine). (origin A2)
- **A3:** The existing reporting protocol (`AgentReport` → `/api/report` → `/api/status`) and 1s frontend poll are reused unchanged apart from the new `Tool` field.
- The repository currently has **no test files**; U1–U4 introduce the first tests. Test-first per global CLAUDE.md TDD directive.
