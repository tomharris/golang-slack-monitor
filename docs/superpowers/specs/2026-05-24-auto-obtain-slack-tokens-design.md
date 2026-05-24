# Design: Auto-obtain Slack tokens from the macOS desktop app

**Date:** 2026-05-24
**Status:** Approved (design) — pending implementation plan
**Inspiration:** [tomharris/dev-rag PR #46](https://github.com/tomharris/dev-rag/pull/46) — `devrag auth slack`

## Problem

Today, running `golang-slack-monitor` requires manually extracting two Slack
credentials via browser DevTools and pasting them into
`~/.slack-monitor/config.json`:

- `xoxc` token (from an API request's `token` parameter)
- `xoxd` token (from the `d` cookie)

This is tedious, error-prone, and the tokens expire (the README lists "no
automatic token refresh (manual re-extraction required)" as a known
limitation). We want the monitor to obtain these credentials automatically from
the **macOS Slack desktop app** and to refresh them transparently when they
expire mid-run.

## Decisions (resolved during brainstorming)

| Fork | Decision |
|------|----------|
| Credential source | macOS **Slack desktop app** (`~/Library/Application Support/Slack/`) |
| Reading the encrypted Cookies SQLite DB | **Relax the stdlib-only constraint**; add a pure-Go SQLite driver (`modernc.org/sqlite`). Keychain password via shell-out to `/usr/bin/security` (no CGo). |
| Delivery / UX | **Auto-acquire on startup + auto-refresh on `token_expired` mid-run** (no separate subcommand) |
| `xoxc` source | **Fetch the workspace page** (`https://<workspace>.slack.com/`) and JSON-parse the boot blob for `api_token`. Requires a one-time non-secret `slack.workspace` subdomain in config. |
| `xoxd` source | Always decrypt the `d` cookie from the Cookies SQLite DB |
| Caching | **Cache derived tokens back into `config.json`** (atomic write) to avoid a Keychain prompt on every startup |

## Non-goals (YAGNI)

- No support for browsers (Chrome/Firefox) as a credential source — desktop app only.
- No reading of the desktop app's LevelDB for `xoxc` — we derive it via the workspace page.
- No multi-workspace auto-discovery — single workspace per config, identified by `slack.workspace`.
- No Windows/Linux support — the auth path is macOS-only and guarded.

## Architecture

Follows Ben Johnson's Standard Package Layout, consistent with the existing
codebase: domain types and interfaces live in the root `monitor` package,
implementations live in subpackages, and `cmd/slack-monitor/main.go` is the only
place implementations are constructed and wired.

### Root package `monitor` (`monitor.go`) — additions

```go
// Credentials are the Slack stealth-mode auth tokens.
type Credentials struct {
    XoxcToken string
    XoxdToken string
}

// Authenticator obtains fresh Slack credentials from the local machine.
type Authenticator interface {
    Authenticate() (Credentials, error)
}

// CredentialStore persists credentials so they survive restarts.
type CredentialStore interface {
    SaveCredentials(Credentials) error
}

// ErrTokenExpired is returned by SlackClient calls when Slack reports the
// session/token is no longer valid (e.g. "token_expired", "invalid_auth").
// The core recognizes this sentinel without knowing Slack's JSON shape.
var ErrTokenExpired = errors.New("slack token expired")
```

- `SlackClient` interface gains `SetCredentials(Credentials)` so refreshed tokens
  can be pushed into the live client without reconstructing it.
- `Config.Slack` gains `Workspace string` (JSON `workspace`), a non-secret subdomain.
- `Monitor` gains injected `Authenticator` and `CredentialStore` dependencies
  (added to `NewMonitor`).

### New package `slackauth/` — implements `monitor.Authenticator`

Depends on root for types; root never imports it. macOS-only (build-tag guarded;
non-darwin builds return a clean "unsupported platform" error).

Internal seams kept small and injectable so OS-touching code can be faked in tests:

1. **`readCookie()`** — locate `~/Library/Application Support/Slack/Cookies`,
   copy to a temp file (to avoid the live app's SQLite lock), open with
   `modernc.org/sqlite`, run `SELECT encrypted_value FROM cookies WHERE name='d'`.
2. **`decryptCookie(encrypted []byte)`** —
   - Get the password: `exec /usr/bin/security find-generic-password -ws "Slack Safe Storage"`.
   - Derive key: hand-rolled PBKDF2-HMAC-SHA1 (salt `saltysalt`, 1003 iterations,
     16-byte key) using stdlib `crypto/hmac` + `crypto/sha1` (PBKDF2 is only in
     stdlib as `crypto/pbkdf2` from Go 1.24; this repo targets 1.20).
   - Decrypt: AES-128-CBC, IV = 16 space bytes (`0x20`), strip the `v10` prefix,
     remove PKCS7 padding.
   - Strip the leading 32-byte Chromium domain-hash prefix if present (newer
     Chromium versions prepend `sha256(domain)` to the plaintext).
3. **`deriveXoxc(workspace, dCookie)`** — GET `https://<workspace>.slack.com/`
   sending the `d` cookie; JSON-parse the embedded boot blob and read the keyed
   `api_token` field (NOT a regex — avoids decoy `xoxc-` placeholders elsewhere
   in the page, per dev-rag PR #46).

`Authenticate()` composes the three steps, then validates the pair via the
existing `slack.Client.TestAuth()` before returning. Any failure returns a
wrapped error pointing the user at the retained manual-extraction fallback.

### `storage/` — implements `monitor.CredentialStore`

A type that knows the `config.json` path and rewrites it atomically: read the
current config JSON, set the two token fields, write to `config.json.tmp`, then
`os.Rename` — the same atomic pattern already used for `state.json`. Preserves
all other config fields and formatting intent.

### `cmd/slack-monitor/main.go` — wiring

- Validation change: `slack.workspace` becomes the required Slack field; the
  tokens become optional (derived if empty).
- On startup: if `config.Slack.XoxcToken`/`XoxdToken` are empty, call
  `Authenticate()`, set them on the client, and persist via the `CredentialStore`.
- Construct the `slackauth` authenticator and `storage` credential store and
  inject both into `monitor.NewMonitor` (for mid-run refresh).

## Data flow

### Startup
```
loadConfig() → tokens empty?
  ├─ no  → use config tokens
  └─ yes → Authenticator.Authenticate()
             → readCookie → decryptCookie (security + PBKDF2 + AES) → xoxd
             → deriveXoxc(workspace, xoxd) → xoxc
             → TestAuth(xoxc, xoxd) validates
           → SlackClient.SetCredentials(creds)
           → CredentialStore.SaveCredentials(creds)   // cache back to config.json
```

### Mid-run refresh
```
Monitor cycle → SlackClient call → ErrTokenExpired?
  └─ yes (once per cycle, guarded) →
       Authenticator.Authenticate()
       → SlackClient.SetCredentials(creds)
       → CredentialStore.SaveCredentials(creds)
       → retry the cycle
     if re-auth also fails → surface error, do not loop
```

## Error handling (fail loud)

- Every layer wraps and returns errors; no layer emits a junk/empty credential.
- Failure messages point at the **retained manual DevTools fallback** (README
  steps kept but demoted).
- `TestAuth` is the backstop: if Slack reshapes its boot blob or the cookie
  format shifts, validation fails loudly rather than the monitor running with a
  bad token.
- Re-auth on expiry is guarded (at most one re-auth attempt per cycle) to avoid
  tight failure loops.
- Non-darwin: `slackauth` returns a clean "unsupported platform" error.
- The `security` shell-out may trigger a one-time macOS Keychain access prompt;
  caching tokens back to `config.json` keeps this to (effectively) first run.

## Invariants preserved

- Stealth-mode auth placement (`xoxc` in `token` param, `xoxd` in `d` cookie,
  plus `d-s` timestamp cookie) is unchanged in `slack/client.go`.
- State write atomicity (tmp + rename) is reused for the config write.
- Check-then-wait polling and graceful shutdown are unchanged; the refresh step
  slots into the existing cycle without overlapping cycles.

## Testing

Table-driven tests beside each package (repo convention):

- `slackauth`: PBKDF2 known-answer vectors; AES-128-CBC decrypt round-trip;
  `v10` prefix + PKCS7 + 32-byte domain-hash stripping; boot-blob JSON parsing
  **including the decoy-`xoxc-` disambiguation case** (high-value test from PR #46).
- OS-touching seams (`security` exec, cookie file path, HTTP fetch) behind small
  internal funcs/interfaces, faked in tests — no real Keychain/network.
- `storage` credential store: round-trip write preserves other config fields;
  atomic tmp+rename behavior.
- `monitor`: a fake `Authenticator` + fake `SlackClient` returning
  `ErrTokenExpired` exercises the refresh-and-retry path (and the guard against
  infinite re-auth).

## Dependency & documentation changes

- `go.mod`: add `modernc.org/sqlite` (pure Go, no CGo — `make build` still needs
  no C toolchain).
- **CLAUDE.md**: update the "stdlib-only / zero third-party dependencies"
  invariant to record that it is deliberately relaxed for this feature, with the
  rationale (reading the encrypted Cookies SQLite DB), so future sessions aren't
  misled. Document the new `slack.workspace` field and the auto-auth/refresh
  behavior.
- **README.md**: lead Step 2 with automatic acquisition (`slack.workspace` +
  desktop app); demote the manual DevTools extraction to a documented fallback;
  remove/curtail the "no automatic token refresh" limitation.
- `config.example.json`: add `slack.workspace`; mark tokens as optional/auto-derived.

## Open risks

- Chromium cookie-encryption format can change across Slack app updates
  (app-bound encryption, prefix changes). Mitigated by `TestAuth` validation and
  the manual fallback, but may need maintenance.
- Reading the Cookies DB while Slack.app holds it open relies on the temp-file
  copy; if the schema/path changes the read fails loud.
