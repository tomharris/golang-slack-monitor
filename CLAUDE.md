# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build                       # Build ./slack-monitor binary from ./cmd/slack-monitor
make test                        # Run all tests with coverage (go test -v -cover ./...)
make run                         # Build + run wrapped in `caffeinate -i` (keeps Mac awake)
make install                     # Build + copy binary to ~/bin
make clean                       # go clean
NTFY_TOPIC=foo make test-message # Send a live test notification to ntfy.sh/foo

go test -v -run TestName ./...   # Run a single test by name
go test ./slack/                 # Test a single package
go build -v ./...                # CI build (mirrors .github/workflows/go.yml)
```

Target toolchain is **Go 1.20** (`go.mod`; CI also pins 1.20). README references 1.21+, but build/CI use 1.20.

## Runtime configuration

The binary reads `~/.slack-monitor/config.json` (see `config.example.json`) and persists
`~/.slack-monitor/state.json`. There are **no CLI flags or env vars** for the app itself —
all config lives in that JSON file. `loadConfig` in `cmd/slack-monitor/main.go` validates
required tokens and applies defaults (poll interval 60s, DMs-only).

## Architecture

This follows **Ben Johnson's Standard Package Layout**. Understanding the dependency direction
is essential before changing anything:

- **Root package `monitor`** (`monitor.go`) — owns ALL domain types (`Message`,
  `Conversation`, `State`, `User`, `Config`) and ALL interfaces (`SlackClient`, `Notifier`,
  `StateStore`). It contains the core polling logic (`Monitor.Run`) but performs no I/O
  directly — it only calls interfaces. Imported elsewhere as the module path
  `github.com/FourPalms/golang-slack-monitor` (package name `monitor`).
- **`slack/`** — implements `SlackClient` against the Slack web API.
- **`notification/`** — implements `Notifier` against ntfy.sh.
- **`storage/`** — implements `StateStore` as atomic JSON file writes.
- **`cmd/slack-monitor/main.go`** — the ONLY place implementations are constructed and wired
  into `monitor.NewMonitor`. Add new dependencies here, not inside the core.

When adding behavior, define the contract as an interface in `monitor.go`, implement it in (or
as) a subpackage, and inject it in `main.go`. Subpackages depend on root for types; root never
imports a subpackage.

**Stdlib-only constraint:** `go.mod` has zero third-party dependencies. This is intentional —
prefer `net/http`, `encoding/json`, etc. over adding modules.

## Non-obvious invariants (do not break)

1. **Slack stealth-mode auth** (`slack/client.go` `makeRequest`): the `xoxc` token goes in the
   `token` request parameter; the `xoxd` token goes in the `d` cookie — NOT reversed. A second
   `d-s` cookie (current Unix time minus 10s) is also required. This was reverse-engineered from
   slackdump/slack-mcp-server; changing the token placement breaks auth silently.

2. **Timestamp tracking** (`monitor.go` `checkConversation`): on first sight of a conversation,
   tracking starts from "now" to avoid notifying about backlog. When a check finds **zero** new
   messages, `state.LastChecked` is intentionally NOT advanced — preserving the real last-message
   timestamp. Don't "tidy" this by always updating it.

3. **Deleted users are skipped** (`checkAllConversations`): conversations with `IsUserDeleted`
   are filtered out (they can't send new messages) and logged separately.

4. **State writes are atomic** (`storage/state.go`): write to `state.json.tmp`, then `os.Rename`.
   Preserve this pattern for any state mutation.

5. **Polling is check-then-wait** (`Monitor.Run`): the next cycle's sleep starts only after the
   current check finishes, preventing overlapping cycles. Shutdown is via `context.Context`
   cancelled on SIGINT/SIGTERM. Notifications are rate-limited to one per 2s
   (`notification/service.go`).

## Conventions

- API wire structs (e.g. `conversationsListResponse`) live unexported in `slack/types.go` and are
  mapped to root `monitor.*` domain types at the package boundary — keep Slack's JSON shape out of
  the core.
- Tests sit beside their package as `*_test.go` and use table-driven cases (see `monitor_test.go`).
