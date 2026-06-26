# Coding Pet Dashboard — Multi-Tool Collection Requirements

**Date:** 2026-06-26
**Status:** Ready for planning
**Scope tier:** Standard

## Problem & Outcome

The dashboard today monitors only CodeBuddy CLI sessions. Developers also run Claude Code, and have no unified view of which of their AI coding sessions — across tools and machines — currently need their attention (a permission grant, a choice, plan approval).

**Outcome:** One dashboard shows live sessions from **both** CodeBuddy and Claude Code across all machines, groups them by tool, and uses the existing yellow flashing alert to flag any session waiting on the user. The product is renamed end-to-end from "CodeBuddy Dashboard" to **Coding Pet Dashboard**.

## Users & Value

The developer running multiple AI coding CLIs on one or more dev machines, watching the dashboard (often on a phone). Value: a single glance tells them which session — regardless of tool — is blocked waiting for them, so no session stalls unnoticed.

## Requirements

### 1. Collect Claude Code sessions

- The agent scans Claude Code session pid files in addition to CodeBuddy.
- Location: `$CLAUDE_CONFIG_DIR/sessions/*.json`. When `CLAUDE_CONFIG_DIR` is unset, fall back to `~/.tclaude/sessions/*.json`.
- The pid-file shape is close to CodeBuddy's (`internal/collector/pidfile.go`) but field names differ — observed fields: `pid`, `sessionId`, `cwd`, `startedAt`, `kind`, `entrypoint`, `status`, `version`, `updatedAt`, `statusUpdatedAt`. There is **no** `lastHeartbeat` field; `updatedAt`/`statusUpdatedAt` are the freshness signals.
- A failed parse of one file skips that file without failing the whole scan (match existing collector behavior).

### 2. Map Claude Code state, including "needs input"

- Claude Code's pid-file `status` field is the primary state source.
- Observed live values: `busy`, `idle`. The user confirmed the field also carries a `waiting_for_*`-style value when a session is blocked on a prompt — **the exact strings and whether they distinguish input vs. approval must be confirmed against a live waiting session at implement-time** (see Assumptions).
- Map the Claude Code status onto the existing `protocol.SessionState` enum (`active`, `waiting_for_input`, `waiting_for_approval`, `terminated`, `unknown`).
- A session in `waiting_for_input` or `waiting_for_approval` drives the **existing** yellow flashing alert — no new alert UI is built.

### 3. Liveness for Claude Code sessions

- Reuse the CodeBuddy liveness model (`collectOne` in `internal/collector/collector.go`): process-alive check plus staleness, adapted to Claude Code's `updatedAt`/`statusUpdatedAt` in place of `lastHeartbeat`.
- A dead/stale Claude Code session is marked `terminated`/`unknown` the same way CodeBuddy sessions are.

### 4. Tag and group sessions by tool

- Add a tool/source field to `protocol.SessionInfo` identifying each session as CodeBuddy or Claude Code.
- The dashboard **groups sessions by tool** within each machine (distinct sections/groups per tool), rather than one flat list.
- Per-machine and global counts (active / waiting / approval / offline in `DashboardResponse`) continue to work across the combined set.

### 5. Full rename to "Coding Pet"

End-to-end rename — user-facing **and** internal:

- UI title and `<h1>` in `internal/server/index.html` → "Coding Pet Dashboard".
- Go module path `github.com/simonlei/codebuddy-dashboard` → coding-pet equivalent (update all imports).
- Binaries `dashboard-agent` / `dashboard-server` and `Makefile` targets → coding-pet naming.
- Other "CodeBuddy"/"codebuddy-dashboard" references in `.go` source, README, and help/usage text, **except** the literal `~/.codebuddy/...` paths that are CodeBuddy's real on-disk locations (those must stay — they point at the tool being monitored, not at this product).
- README updated to describe multi-tool (CodeBuddy + Claude Code) monitoring.

## Scope Boundaries

**In scope:** Claude Code collection via pid files, status→state mapping reusing the existing alert, tool tagging + grouped display, full product rename including module and binaries.

**Deferred / not now:**
- JSONL deep-parsing for Claude Code (its `message.content[]` schema, nested `tool_use`/`stop_reason`) — only the contingency fallback if the `status` field proves insufficient (see Assumptions). Not built unless needed.
- Monitoring any third coding tool beyond CodeBuddy and Claude Code.
- Changes to the alert visual itself (color, animation) — reused as-is.

## Dependencies / Assumptions

- **A1 (must verify):** Claude Code's pid-file `status` field emits explicit waiting state(s) when blocked on a prompt. Directly observed only `busy`/`idle` on live sessions; user confirmed a waiting value exists. Implementation must capture a live waiting session to learn the exact string(s) and whether input vs. approval is distinguishable. If `status` alone can't distinguish them, fall back to JSONL last-entry parsing for Claude Code (the input-vs-approval split is the only thing at risk — the alert still fires on any waiting value).
- **A2:** `CLAUDE_CONFIG_DIR` override + `~/.tclaude` fallback is the correct discovery path (confirmed live on this machine).
- Existing reporting protocol (`AgentReport` → `/api/report` → `/api/status`) and the 1s frontend poll are reused unchanged aside from the new tool field.

## Success Criteria

1. With both a CodeBuddy and a Claude Code session running, the dashboard shows both, grouped by tool, per machine.
2. A Claude Code session blocked on a permission/choice/plan prompt flashes yellow, same as CodeBuddy.
3. Terminated/idle Claude Code sessions are reflected correctly (not shown as waiting).
4. No occurrence of "CodeBuddy Dashboard" branding, `codebuddy-dashboard` module path, or `dashboard-agent`/`dashboard-server` binary names remains; `~/.codebuddy/` monitoring paths are intentionally retained.
5. `make build` produces the renamed binaries and the agent collects from both tools.

## Outstanding Questions

- New module path / binary names: exact strings (e.g. `coding-pet-dashboard`, `coding-pet-agent`/`coding-pet-server`?) — to settle at planning.
- Grouping UI: collapse empty tool groups, or always show both headers per machine?
