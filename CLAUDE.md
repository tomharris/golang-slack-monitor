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

Target toolchain is **Go 1.25** (`go.mod`; CI also pins 1.25). The `modernc.org/sqlite`
dependency requires Go 1.25, which drove this baseline.

## Runtime configuration

The binary reads `~/.slack-monitor/config.json` (see `config.example.json`) and persists
`~/.slack-monitor/state.json`. There are **no CLI flags or env vars** for the app itself —
all config lives in that JSON file. `loadConfig` in `cmd/slack-monitor/main.go` validates
required fields (`slack.workspace`, `notifications.ntfy_topic`) and applies defaults (poll
interval 60s, DMs-only). The `xoxc`/`xoxd` tokens are **optional**: when absent they are
auto-derived from the macOS Slack desktop app (see `slackauth/`) and cached back into
`config.json`.

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
- **`storage/`** — implements `StateStore` (`state.json`) and `CredentialStore` (`config.json`)
  as atomic JSON file writes.
- **`slackauth/`** — implements `Authenticator`; obtains `xoxc`/`xoxd` from the macOS Slack
  desktop app (decrypts the `d` cookie via Keychain + AES, derives `xoxc` from the workspace
  page). macOS-only (`darwin` build-tagged glue; non-darwin returns `ErrUnsupportedPlatform`).
- **`cmd/slack-monitor/main.go`** — the ONLY place implementations are constructed and wired
  into `monitor.NewMonitor`. Add new dependencies here, not inside the core.

When adding behavior, define the contract as an interface in `monitor.go`, implement it in (or
as) a subpackage, and inject it in `main.go`. Subpackages depend on root for types; root never
imports a subpackage.

**Near-stdlib constraint:** `go.mod` has exactly one direct third-party dependency —
`modernc.org/sqlite` (pure Go, no CGo) — used solely by `slackauth/` to read the macOS Slack
desktop app's Cookies database. Everything else stays stdlib (`net/http`, `encoding/json`,
hand-rolled PBKDF2, etc.). Do not add further modules without cause.

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

6. **Token auto-refresh** (`monitor.go` `refreshCredentials`): when a `SlackClient` call returns
   `ErrTokenExpired`, the run loop re-authenticates via the injected `Authenticator`, pushes new
   tokens into the client, validates, and persists them through `CredentialStore`. `slackauth`
   never imports `slack`; validation lives in the caller. Cookie decryption params (`saltysalt`,
   1003 iters, AES-128-CBC, `v10` prefix, optional 32-byte domain-hash prefix) are exact —
   don't tweak them.

## Conventions

- API wire structs (e.g. `conversationsListResponse`) live unexported in `slack/types.go` and are
  mapped to root `monitor.*` domain types at the package boundary — keep Slack's JSON shape out of
  the core.
- Tests sit beside their package as `*_test.go` and use table-driven cases (see `monitor_test.go`).
