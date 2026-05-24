# Auto-obtain Slack Tokens Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the monitor obtain Slack `xoxc`/`xoxd` credentials automatically from the macOS Slack desktop app on startup, and refresh them transparently when they expire mid-run — eliminating manual DevTools extraction.

**Architecture:** Ben Johnson's Standard Package Layout. New domain types/interfaces (`Credentials`, `Authenticator`, `CredentialStore`, `ErrTokenExpired`) go in the root `monitor` package; a new `slackauth/` package implements credential extraction (decrypt the `d` cookie from the desktop app's SQLite Cookies DB, then derive `xoxc` from the workspace page); `storage/` gains a `config.json` writer; `cmd/slack-monitor/main.go` wires it all and `monitor.Run` performs auto-refresh on `ErrTokenExpired`.

**Tech Stack:** Go (toolchain bumped to 1.23), `modernc.org/sqlite` (pure-Go, no CGo), stdlib `crypto/aes`, `crypto/cipher`, `crypto/hmac`, `crypto/sha1`, `net/http`, `encoding/json`, `os/exec` (macOS `security`).

**Design note (deviation from spec):** Per-package decoupling, `slackauth` does NOT import `slack`. `Authenticate()` returns credentials; the *caller* validates them by calling `SlackClient.SetCredentials` then `TestAuth`. Validation lives in `main.go` (startup) and `Monitor.refreshCredentials` (mid-run).

**Platform strategy:** Crypto, HTTP/xoxc, and SQLite-read units are cross-platform so they test on Linux CI. Only the Keychain password lookup and the default Cookies path are `darwin`-gated; non-darwin returns `ErrUnsupportedPlatform`.

---

## File Structure

- `go.mod`, `.github/workflows/go.yml` — bump Go to 1.23; add `modernc.org/sqlite`.
- `monitor.go` (modify) — add `Credentials`, `Authenticator`, `CredentialStore`, `ErrTokenExpired`, `Config.Slack.Workspace`, `SlackClient.SetCredentials`; extend `Monitor` + `NewMonitor`; add `refreshCredentials` + auto-refresh in `Run`/`checkAllConversations`.
- `slack/client.go` (modify) — `SetCredentials`; map `token_expired`/`invalid_auth` responses to `monitor.ErrTokenExpired`.
- `slack/client_test.go` (modify) — tests for the above.
- `storage/config.go` (create) — `ConfigStore` implementing `monitor.CredentialStore` (atomic `config.json` rewrite).
- `storage/config_test.go` (create) — round-trip + field-preservation tests.
- `slackauth/crypto.go` (create) — `pbkdf2SHA1`, `decryptCookie`.
- `slackauth/crypto_test.go` (create) — RFC 6070 PBKDF2 vectors, AES round-trip, prefix/padding/url-decode.
- `slackauth/xoxc.go` (create) — `extractAPIToken`, `deriveXoxc`.
- `slackauth/xoxc_test.go` (create) — decoy disambiguation, httptest fetch.
- `slackauth/cookie.go` (create) — `readEncryptedDCookie` (copy + `modernc.org/sqlite` query).
- `slackauth/cookie_test.go` (create) — temp DB round-trip.
- `slackauth/keychain_darwin.go` (create) — `keychainPassword`, `defaultCookiePath`.
- `slackauth/keychain_other.go` (create) — `ErrUnsupportedPlatform` stubs.
- `slackauth/auth.go` (create) — `Authenticator` struct + `Authenticate` + `NewAuthenticator`.
- `slackauth/auth_test.go` (create) — composition + error propagation with fakes.
- `cmd/slack-monitor/main.go` (modify) — `loadConfig` validation (workspace required, tokens optional); startup auth; wire `slackauth` + `storage.ConfigStore` into `NewMonitor`.
- `CLAUDE.md`, `README.md`, `config.example.json` (modify) — docs.

---

## Task 1: Toolchain & dependency setup

**Files:**
- Modify: `go.mod`
- Modify: `.github/workflows/go.yml`

- [ ] **Step 1: Bump the Go directive in `go.mod`**

Change the `go` line in `go.mod` from `go 1.20` to:

```
go 1.23
```

- [ ] **Step 2: Bump CI Go version**

In `.github/workflows/go.yml`, change `go-version: '1.20'` to:

```yaml
        go-version: '1.23'
```

- [ ] **Step 3: Add the SQLite driver**

Run: `go get modernc.org/sqlite@latest`
Expected: `go.mod` gains a `require modernc.org/sqlite vX.Y.Z` line and `go.sum` is updated.

- [ ] **Step 4: Verify the module still builds**

Run: `go build ./...`
Expected: builds with no errors (no code uses the driver yet; this confirms the dependency resolves under Go 1.23).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .github/workflows/go.yml
git commit -m "build: bump to Go 1.23 and add modernc.org/sqlite"
```

---

## Task 2: Core domain types & interfaces (`monitor` package)

**Files:**
- Modify: `monitor.go`
- Test: `monitor_test.go` (compile-only check; behavior tested in Task 8)

- [ ] **Step 1: Add an import for `errors`**

In `monitor.go`, update the import block (currently `context`, `fmt`, `log`, `time`) to add `errors`:

```go
import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)
```

- [ ] **Step 2: Add `Credentials`, the sentinel error, and the two new interfaces**

Add near the other type/interface declarations in `monitor.go`:

```go
// Credentials are the Slack stealth-mode auth tokens.
type Credentials struct {
	XoxcToken string
	XoxdToken string
}

// ErrTokenExpired is returned by SlackClient calls when Slack reports the
// session is no longer valid (e.g. "token_expired" or "invalid_auth"). The
// core recognizes this sentinel without knowing Slack's JSON shape.
var ErrTokenExpired = errors.New("slack token expired")

// Authenticator obtains fresh Slack credentials from the local machine.
type Authenticator interface {
	Authenticate() (Credentials, error)
}

// CredentialStore persists credentials so they survive restarts.
type CredentialStore interface {
	SaveCredentials(Credentials) error
}
```

- [ ] **Step 3: Add `Workspace` to the config and `SetCredentials` to `SlackClient`**

In `monitor.go`, add the `Workspace` field to the `Slack` struct (place it after `XoxdToken`):

```go
	Slack struct {
		XoxcToken        string `json:"xoxc_token"`
		XoxdToken        string `json:"xoxd_token"`
		Workspace        string `json:"workspace"`
		WorkspaceID      string `json:"workspace_id"`
		PollIntervalSecs int    `json:"poll_interval_seconds"`
	} `json:"slack"`
```

Add `SetCredentials` to the `SlackClient` interface (after `GetAuthenticatedUserID`):

```go
	// GetAuthenticatedUserID returns the ID of the authenticated user
	GetAuthenticatedUserID() string

	// SetCredentials replaces the client's auth tokens (used on refresh)
	SetCredentials(Credentials)
```

- [ ] **Step 4: Extend `Monitor` and `NewMonitor` with the new dependencies**

Replace the `Monitor` struct and `NewMonitor` in `monitor.go` with:

```go
// Monitor represents the core monitoring logic
type Monitor struct {
	slackClient   SlackClient
	notifier      Notifier
	stateStore    StateStore
	authenticator Authenticator
	credStore     CredentialStore
	config        *Config
	userCache     map[string]string // userID -> display name cache
}

// NewMonitor creates a new Monitor instance. authenticator and credStore may be
// nil, in which case automatic token refresh is disabled.
func NewMonitor(slackClient SlackClient, notifier Notifier, stateStore StateStore, authenticator Authenticator, credStore CredentialStore, config *Config) *Monitor {
	return &Monitor{
		slackClient:   slackClient,
		notifier:      notifier,
		stateStore:    stateStore,
		authenticator: authenticator,
		credStore:     credStore,
		config:        config,
		userCache:     make(map[string]string),
	}
}
```

- [ ] **Step 5: Verify it compiles**

Run: `go build ./...`
Expected: FAILS — `cmd/slack-monitor/main.go` still calls the old 4-arg `NewMonitor` and `slack.Client` lacks `SetCredentials`. This is expected; those are fixed in Tasks 3 and 9. Confirm the failure is ONLY those two call sites:

Run: `go vet ./... 2>&1 | head`
Expected: errors reference `cmd/slack-monitor/main.go` (NewMonitor arg count) and `*slack.Client` not implementing `SlackClient`.

- [ ] **Step 6: Verify the package's own tests still compile/pass**

Run: `go test ./ -run TestFormatNotification -v`
Expected: PASS (the root package compiles on its own; `formatNotification` is unchanged).

- [ ] **Step 7: Commit**

```bash
git add monitor.go
git commit -m "feat: add Credentials, Authenticator, CredentialStore, ErrTokenExpired to core"
```

---

## Task 3: Slack client — `SetCredentials` + expiry detection

**Files:**
- Modify: `slack/client.go`
- Test: `slack/client_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `slack/client_test.go`:

```go
func TestSetCredentials(t *testing.T) {
	c := NewClient("old-xoxc", "old-xoxd")
	c.SetCredentials(monitor.Credentials{XoxcToken: "new-xoxc", XoxdToken: "new-xoxd"})
	if c.xoxcToken != "new-xoxc" || c.xoxdToken != "new-xoxd" {
		t.Fatalf("SetCredentials did not update tokens: got %q/%q", c.xoxcToken, c.xoxdToken)
	}
}

func TestSlackErrorMapsToExpired(t *testing.T) {
	tests := []struct {
		apiError string
		wantExp  bool
	}{
		{"token_expired", true},
		{"invalid_auth", true},
		{"channel_not_found", false},
		{"", false},
	}
	for _, tt := range tests {
		err := slackError(tt.apiError)
		if err == nil {
			t.Fatalf("slackError(%q) = nil, want error", tt.apiError)
		}
		if got := errors.Is(err, monitor.ErrTokenExpired); got != tt.wantExp {
			t.Errorf("slackError(%q): errors.Is(ErrTokenExpired)=%v, want %v", tt.apiError, got, tt.wantExp)
		}
	}
}
```

Ensure `slack/client_test.go`'s import block includes `"errors"` and `"github.com/FourPalms/golang-slack-monitor"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./slack/ -run 'TestSetCredentials|TestSlackErrorMapsToExpired' -v`
Expected: FAIL — `c.SetCredentials` undefined and `slackError` undefined.

- [ ] **Step 3: Add `SetCredentials` and a shared error helper**

In `slack/client.go` (which already imports `fmt` and the `monitor` module), add these methods/functions — no new imports are needed:

```go
// SetCredentials replaces the client's auth tokens (used on refresh).
func (c *Client) SetCredentials(creds monitor.Credentials) {
	c.xoxcToken = creds.XoxcToken
	c.xoxdToken = creds.XoxdToken
}

// slackError converts a Slack API error string into an error, mapping
// session-expiry errors to monitor.ErrTokenExpired so the core can react.
func slackError(apiError string) error {
	switch apiError {
	case "token_expired", "invalid_auth":
		return fmt.Errorf("%w (%s)", monitor.ErrTokenExpired, apiError)
	default:
		return fmt.Errorf("Slack API error: %s", apiError)
	}
}
```

- [ ] **Step 4: Route existing API error returns through `slackError`**

In `slack/client.go`, replace each occurrence of:

```go
		return nil, fmt.Errorf("Slack API error: %s", response.Error)
```

with:

```go
		return nil, slackError(response.Error)
```

(There are three: in `GetDMConversations`, `GetConversationHistory`, `GetUserInfo`.) In `TestAuth`, replace:

```go
		return "", fmt.Errorf("Slack authentication failed: %s", response.Error)
```

with:

```go
		return "", slackError(response.Error)
```

Keep the `errors` import only if used; if `go vet` flags it as unused, remove it (the helper uses `fmt`, not `errors`).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./slack/ -v`
Expected: PASS (new tests plus existing ones). If `go vet` reports `"errors"` unused in `client_test.go`, that means a test stopped referencing `errors.Is`; keep it — both new tests use it.

- [ ] **Step 6: Commit**

```bash
git add slack/client.go slack/client_test.go
git commit -m "feat: slack client SetCredentials and ErrTokenExpired mapping"
```

---

## Task 4: `storage.ConfigStore` — atomic `config.json` writer

**Files:**
- Create: `storage/config.go`
- Test: `storage/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `storage/config_test.go`:

```go
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/FourPalms/golang-slack-monitor"
)

func TestSaveCredentialsPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	initial := `{
  "slack": {"xoxc_token": "", "xoxd_token": "", "workspace": "acme", "poll_interval_seconds": 30},
  "notifications": {"ntfy_topic": "secret-topic"},
  "monitor": {"dms_only": true}
}`
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	cs := NewConfigStore(path)
	if err := cs.SaveCredentials(monitor.Credentials{XoxcToken: "xoxc-new", XoxdToken: "xoxd-new"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	var cfg monitor.Config
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("reread: %v", err)
	}
	if cfg.Slack.XoxcToken != "xoxc-new" || cfg.Slack.XoxdToken != "xoxd-new" {
		t.Errorf("tokens not written: %q/%q", cfg.Slack.XoxcToken, cfg.Slack.XoxdToken)
	}
	if cfg.Slack.Workspace != "acme" || cfg.Slack.PollIntervalSecs != 30 {
		t.Errorf("slack fields not preserved: %+v", cfg.Slack)
	}
	if cfg.Notifications.NtfyTopic != "secret-topic" || cfg.Monitor.DMsOnly != true {
		t.Errorf("other sections not preserved: %+v", cfg)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./storage/ -run TestSaveCredentialsPreservesOtherFields -v`
Expected: FAIL — `NewConfigStore` undefined.

- [ ] **Step 3: Implement `ConfigStore`**

Create `storage/config.go`:

```go
package storage

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/FourPalms/golang-slack-monitor"
)

// ConfigStore implements monitor.CredentialStore by rewriting config.json.
type ConfigStore struct {
	configPath string
}

// NewConfigStore creates a credential store backed by the given config.json path.
func NewConfigStore(configPath string) *ConfigStore {
	return &ConfigStore{configPath: configPath}
}

// SaveCredentials loads the current config, updates the two token fields, and
// writes the file back atomically (tmp + rename), preserving all other fields.
func (cs *ConfigStore) SaveCredentials(creds monitor.Credentials) error {
	data, err := os.ReadFile(cs.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config monitor.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	config.Slack.XoxcToken = creds.XoxcToken
	config.Slack.XoxdToken = creds.XoxdToken

	out, err := json.MarshalIndent(&config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tempPath := cs.configPath + ".tmp"
	if err := os.WriteFile(tempPath, out, 0600); err != nil {
		return fmt.Errorf("failed to write temporary config file: %w", err)
	}
	if err := os.Rename(tempPath, cs.configPath); err != nil {
		return fmt.Errorf("failed to rename config file: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./storage/ -v`
Expected: PASS (new test plus existing state tests).

- [ ] **Step 5: Commit**

```bash
git add storage/config.go storage/config_test.go
git commit -m "feat: storage ConfigStore for atomic credential persistence"
```

---

## Task 5: `slackauth` crypto — PBKDF2 + cookie decryption

**Files:**
- Create: `slackauth/crypto.go`
- Test: `slackauth/crypto_test.go`

- [ ] **Step 1: Write the failing tests**

Create `slackauth/crypto_test.go`:

```go
package slackauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

// RFC 6070 PBKDF2-HMAC-SHA1 known-answer vectors.
func TestPBKDF2SHA1_RFC6070(t *testing.T) {
	tests := []struct {
		password, salt string
		iter, keyLen   int
		wantHex        string
	}{
		{"password", "salt", 1, 20, "0c60c80f961f0e71f3a9b524af6012062fe037a6"},
		{"password", "salt", 2, 20, "ea6c014dc72d6f8ccd1ed92ace1d41f0d8de8957"},
	}
	for _, tt := range tests {
		got := pbkdf2SHA1([]byte(tt.password), []byte(tt.salt), tt.iter, tt.keyLen)
		if hex.EncodeToString(got) != tt.wantHex {
			t.Errorf("pbkdf2SHA1(%q,%q,%d,%d) = %x, want %s", tt.password, tt.salt, tt.iter, tt.keyLen, got, tt.wantHex)
		}
	}
}

func TestDecryptCookie_RoundTrip(t *testing.T) {
	// Derive the same key decryptCookie will derive for password "peanuts".
	key := pbkdf2SHA1([]byte("peanuts"), []byte("saltysalt"), 1003, 16)
	plain := "xoxd-abc%2Fdef" // url-encoded value as stored

	block, _ := aes.NewCipher(key)
	iv := bytes.Repeat([]byte{' '}, 16)
	// PKCS7 pad
	padded := pkcs7Pad([]byte(plain), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	encrypted := append([]byte("v10"), ct...)

	got, err := decryptCookie(encrypted, "peanuts")
	if err != nil {
		t.Fatalf("decryptCookie: %v", err)
	}
	if got != "xoxd-abc/def" { // url-decoded
		t.Errorf("decryptCookie = %q, want %q", got, "xoxd-abc/def")
	}
}

func TestDecryptCookie_StripsDomainHashPrefix(t *testing.T) {
	key := pbkdf2SHA1([]byte("peanuts"), []byte("saltysalt"), 1003, 16)
	// 32-byte domain hash prefix followed by the real value.
	plain := append(bytes.Repeat([]byte{0xAB}, 32), []byte("xoxd-zzz")...)

	block, _ := aes.NewCipher(key)
	iv := bytes.Repeat([]byte{' '}, 16)
	padded := pkcs7Pad(plain, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	encrypted := append([]byte("v10"), ct...)

	got, err := decryptCookie(encrypted, "peanuts")
	if err != nil {
		t.Fatalf("decryptCookie: %v", err)
	}
	if got != "xoxd-zzz" {
		t.Errorf("decryptCookie = %q, want %q", got, "xoxd-zzz")
	}
}

// helper mirrored from implementation for test setup
func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./slackauth/ -run 'TestPBKDF2SHA1_RFC6070|TestDecryptCookie' -v`
Expected: FAIL — `pbkdf2SHA1` and `decryptCookie` undefined.

- [ ] **Step 3: Implement the crypto**

Create `slackauth/crypto.go`:

```go
package slackauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
)

// pbkdf2SHA1 derives a key of keyLen bytes using PBKDF2-HMAC-SHA1.
// (crypto/pbkdf2 only exists in stdlib from Go 1.24; this targets 1.23.)
func pbkdf2SHA1(password, salt []byte, iter, keyLen int) []byte {
	const hLen = sha1.Size // 20
	numBlocks := (keyLen + hLen - 1) / hLen
	var dk []byte
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha1.New, password)
		mac.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// decryptCookie decrypts a Chromium-style encrypted cookie value (macOS) using
// the "Slack Safe Storage" Keychain password, returning the url-decoded value.
func decryptCookie(encrypted []byte, password string) (string, error) {
	if !bytes.HasPrefix(encrypted, []byte("v10")) {
		return "", fmt.Errorf("unexpected cookie encryption version (want v10 prefix)")
	}
	ct := encrypted[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length %d", len(ct))
	}

	key := pbkdf2SHA1([]byte(password), []byte("saltysalt"), 1003, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)

	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}

	value := string(plain)
	// Newer Chromium prepends a 32-byte SHA256 domain hash to the plaintext.
	if !strings.HasPrefix(value, "xoxd-") && len(plain) > 32 {
		value = string(plain[32:])
	}

	unescaped, err := url.QueryUnescape(value)
	if err != nil {
		return "", fmt.Errorf("url-unescape cookie: %w", err)
	}
	if !strings.HasPrefix(unescaped, "xoxd-") {
		return "", fmt.Errorf("decrypted cookie does not look like an xoxd token")
	}
	return unescaped, nil
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded length %d", len(b))
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	return b[:len(b)-n], nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./slackauth/ -run 'TestPBKDF2SHA1_RFC6070|TestDecryptCookie' -v`
Expected: PASS (all three crypto tests).

- [ ] **Step 5: Commit**

```bash
git add slackauth/crypto.go slackauth/crypto_test.go
git commit -m "feat: slackauth cookie decryption (PBKDF2 + AES-128-CBC)"
```

---

## Task 6: `slackauth` xoxc derivation

**Files:**
- Create: `slackauth/xoxc.go`
- Test: `slackauth/xoxc_test.go`

- [ ] **Step 1: Write the failing tests**

Create `slackauth/xoxc_test.go`:

```go
package slackauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const bootHTML = `<html><body>
<script>var decoy = "xoxc-DECOY-NOT-THE-TOKEN";</script>
<script>boot_data = {"team_id":"T123","api_token":"xoxc-real-1234-5678","other":"x"};</script>
</body></html>`

func TestExtractAPIToken_PrefersKeyedValue(t *testing.T) {
	got, err := extractAPIToken(bootHTML)
	if err != nil {
		t.Fatalf("extractAPIToken: %v", err)
	}
	if got != "xoxc-real-1234-5678" {
		t.Errorf("extractAPIToken = %q, want xoxc-real-1234-5678 (decoy must be ignored)", got)
	}
}

func TestExtractAPIToken_NotFound(t *testing.T) {
	if _, err := extractAPIToken("<html>no token here</html>"); err == nil {
		t.Error("expected error when api_token absent, got nil")
	}
}

func TestDeriveXoxc_SendsCookieAndExtracts(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("d"); err == nil {
			gotCookie = c.Value
		}
		w.Write([]byte(bootHTML))
	}))
	defer srv.Close()

	token, err := deriveXoxc(srv.Client(), srv.URL, "xoxd-my-cookie")
	if err != nil {
		t.Fatalf("deriveXoxc: %v", err)
	}
	if token != "xoxc-real-1234-5678" {
		t.Errorf("deriveXoxc token = %q", token)
	}
	if gotCookie != "xoxd-my-cookie" {
		t.Errorf("d cookie not sent: got %q", gotCookie)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./slackauth/ -run 'TestExtractAPIToken|TestDeriveXoxc' -v`
Expected: FAIL — `extractAPIToken` and `deriveXoxc` undefined.

- [ ] **Step 3: Implement xoxc derivation**

Create `slackauth/xoxc.go`:

```go
package slackauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// extractAPIToken finds the value of the "api_token" key in the workspace
// boot HTML. It keys on "api_token" (not a bare xoxc- match) so decoy
// placeholders elsewhere in the page are ignored, then JSON-decodes the
// string value so escapes are handled correctly.
func extractAPIToken(html string) (string, error) {
	const key = `"api_token"`
	idx := strings.Index(html, key)
	if idx < 0 {
		return "", fmt.Errorf("api_token not found in workspace page")
	}
	rest := html[idx+len(key):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return "", fmt.Errorf("malformed api_token entry")
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if len(rest) == 0 || rest[0] != '"' {
		return "", fmt.Errorf("api_token value is not a string")
	}
	// Find the closing quote of the JSON string (skipping escaped quotes).
	end := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\\' {
			i++
			continue
		}
		if rest[i] == '"' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("unterminated api_token string")
	}
	var token string
	if err := json.Unmarshal([]byte(rest[:end+1]), &token); err != nil {
		return "", fmt.Errorf("decode api_token: %w", err)
	}
	if !strings.HasPrefix(token, "xoxc-") {
		return "", fmt.Errorf("api_token value is not an xoxc token")
	}
	return token, nil
}

// deriveXoxc fetches the workspace base URL with the d cookie and returns the
// embedded xoxc api_token. baseURL is e.g. https://acme.slack.com.
func deriveXoxc(client *http.Client, baseURL, xoxdCookie string) (string, error) {
	req, err := http.NewRequest("GET", baseURL+"/", nil)
	if err != nil {
		return "", fmt.Errorf("build workspace request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "d", Value: xoxdCookie})
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch workspace page: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read workspace page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("workspace page returned status %d", resp.StatusCode)
	}
	return extractAPIToken(string(body))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./slackauth/ -run 'TestExtractAPIToken|TestDeriveXoxc' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add slackauth/xoxc.go slackauth/xoxc_test.go
git commit -m "feat: slackauth xoxc derivation from workspace boot blob"
```

---

## Task 7: `slackauth` cookie read (SQLite)

**Files:**
- Create: `slackauth/cookie.go`
- Test: `slackauth/cookie_test.go`

- [ ] **Step 1: Write the failing test**

Create `slackauth/cookie_test.go`:

```go
package slackauth

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestReadEncryptedDCookie(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "Cookies")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, encrypted_value BLOB)`); err != nil {
		t.Fatal(err)
	}
	want := []byte("v10ENCRYPTEDBYTES")
	if _, err := db.Exec(`INSERT INTO cookies (host_key, name, encrypted_value) VALUES (?, ?, ?)`,
		".slack.com", "d", want); err != nil {
		t.Fatal(err)
	}
	// A decoy cookie that must be ignored.
	if _, err := db.Exec(`INSERT INTO cookies (host_key, name, encrypted_value) VALUES (?, ?, ?)`,
		".example.com", "other", []byte("nope")); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got, err := readEncryptedDCookie(dbPath)
	if err != nil {
		t.Fatalf("readEncryptedDCookie: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadEncryptedDCookie_Missing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "Cookies")
	db, _ := sql.Open("sqlite", dbPath)
	db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, encrypted_value BLOB)`)
	db.Close()

	if _, err := readEncryptedDCookie(dbPath); err == nil {
		t.Error("expected error when no d cookie present, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./slackauth/ -run TestReadEncryptedDCookie -v`
Expected: FAIL — `readEncryptedDCookie` undefined.

- [ ] **Step 3: Implement the cookie reader**

Create `slackauth/cookie.go`:

```go
package slackauth

import (
	"database/sql"
	"fmt"
	"io"
	"os"

	_ "modernc.org/sqlite"
)

// readEncryptedDCookie copies the Chromium Cookies SQLite DB (to avoid the live
// app's file lock), opens it read-only, and returns the encrypted_value of the
// Slack "d" cookie.
func readEncryptedDCookie(dbPath string) ([]byte, error) {
	tmp, err := copyToTemp(dbPath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)

	db, err := sql.Open("sqlite", "file:"+tmp+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open cookies db: %w", err)
	}
	defer db.Close()

	row := db.QueryRow(
		`SELECT encrypted_value FROM cookies WHERE name = 'd' AND host_key LIKE '%slack.com' ORDER BY LENGTH(encrypted_value) DESC LIMIT 1`)
	var enc []byte
	if err := row.Scan(&enc); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no Slack 'd' cookie found in %s", dbPath)
		}
		return nil, fmt.Errorf("query d cookie: %w", err)
	}
	if len(enc) == 0 {
		return nil, fmt.Errorf("Slack 'd' cookie has empty encrypted_value")
	}
	return enc, nil
}

func copyToTemp(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open cookies file: %w", err)
	}
	defer in.Close()

	out, err := os.CreateTemp("", "slack-cookies-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create temp cookies file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", fmt.Errorf("copy cookies file: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./slackauth/ -run TestReadEncryptedDCookie -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add slackauth/cookie.go slackauth/cookie_test.go
git commit -m "feat: slackauth read encrypted d cookie from Cookies SQLite"
```

---

## Task 8: `slackauth` platform glue + `Authenticator` composition

**Files:**
- Create: `slackauth/keychain_darwin.go`
- Create: `slackauth/keychain_other.go`
- Create: `slackauth/auth.go`
- Test: `slackauth/auth_test.go`

- [ ] **Step 1: Write the failing test**

Create `slackauth/auth_test.go`:

```go
package slackauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticate_Composition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bootHTML)) // from xoxc_test.go
	}))
	defer srv.Close()

	a := &Authenticator{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
		readEncrypted: func() ([]byte, error) {
			// "v10" + AES-CBC of url-encoded xoxd, password "peanuts".
			return makeEncryptedCookie(t, "peanuts", "xoxd-test%2Dvalue"), nil
		},
		keychainPassword: func() (string, error) { return "peanuts", nil },
	}

	creds, err := a.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if creds.XoxcToken != "xoxc-real-1234-5678" {
		t.Errorf("xoxc = %q", creds.XoxcToken)
	}
	if creds.XoxdToken != "xoxd-test-value" {
		t.Errorf("xoxd = %q", creds.XoxdToken)
	}
}

func TestAuthenticate_PropagatesCookieError(t *testing.T) {
	sentinel := errors.New("keyring locked")
	a := &Authenticator{
		baseURL:          "http://unused",
		httpClient:       http.DefaultClient,
		readEncrypted:    func() ([]byte, error) { return nil, sentinel },
		keychainPassword: func() (string, error) { return "peanuts", nil },
	}
	if _, err := a.Authenticate(); !errors.Is(err, sentinel) {
		t.Errorf("Authenticate err = %v, want wrap of sentinel", err)
	}
}
```

Add this helper to `slackauth/crypto_test.go` (it reuses the existing imports there plus `testing`):

```go
func makeEncryptedCookie(t *testing.T, password, urlEncodedValue string) []byte {
	t.Helper()
	key := pbkdf2SHA1([]byte(password), []byte("saltysalt"), 1003, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{' '}, 16)
	padded := pkcs7Pad([]byte(urlEncodedValue), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return append([]byte("v10"), ct...)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./slackauth/ -run TestAuthenticate -v`
Expected: FAIL — `Authenticator` type and its fields undefined.

- [ ] **Step 3: Implement the platform glue**

Create `slackauth/keychain_darwin.go`:

```go
//go:build darwin

package slackauth

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// keychainPassword reads the "Slack Safe Storage" password from the macOS
// Keychain. This may surface a one-time OS access prompt.
func keychainPassword() (string, error) {
	out, err := exec.Command("/usr/bin/security",
		"find-generic-password", "-ws", "Slack Safe Storage").Output()
	if err != nil {
		return "", fmt.Errorf("read Slack Safe Storage password from Keychain: %w", err)
	}
	pw := strings.TrimRight(string(out), "\n")
	if pw == "" {
		return "", fmt.Errorf("empty Slack Safe Storage password from Keychain")
	}
	return pw, nil
}

// defaultCookiePath is the macOS Slack desktop app Cookies DB location.
func defaultCookiePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Slack", "Cookies"), nil
}
```

Create `slackauth/keychain_other.go`:

```go
//go:build !darwin

package slackauth

import "errors"

// ErrUnsupportedPlatform indicates automatic token acquisition is macOS-only.
var ErrUnsupportedPlatform = errors.New("automatic Slack token acquisition is only supported on macOS")

func keychainPassword() (string, error)      { return "", ErrUnsupportedPlatform }
func defaultCookiePath() (string, error)      { return "", ErrUnsupportedPlatform }
```

- [ ] **Step 4: Implement the `Authenticator`**

Create `slackauth/auth.go`:

```go
package slackauth

import (
	"fmt"
	"net/http"
	"time"

	"github.com/FourPalms/golang-slack-monitor"
)

// Authenticator obtains Slack credentials from the local macOS desktop app.
// It implements monitor.Authenticator. The seams (readEncrypted,
// keychainPassword, httpClient, baseURL) are fields so tests can inject fakes.
type Authenticator struct {
	baseURL          string                 // e.g. https://acme.slack.com
	httpClient       *http.Client
	readEncrypted    func() ([]byte, error) // returns the encrypted d-cookie blob
	keychainPassword func() (string, error)
}

// NewAuthenticator wires the real macOS implementations for the given workspace
// subdomain (e.g. "acme" for acme.slack.com).
func NewAuthenticator(workspace string) *Authenticator {
	return &Authenticator{
		baseURL:    fmt.Sprintf("https://%s.slack.com", workspace),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		readEncrypted: func() ([]byte, error) {
			path, err := defaultCookiePath()
			if err != nil {
				return nil, err
			}
			return readEncryptedDCookie(path)
		},
		keychainPassword: keychainPassword,
	}
}

// Authenticate reads and decrypts the d cookie, then derives the xoxc token.
// It does NOT validate against Slack; the caller validates via SlackClient.
func (a *Authenticator) Authenticate() (monitor.Credentials, error) {
	encrypted, err := a.readEncrypted()
	if err != nil {
		return monitor.Credentials{}, fmt.Errorf("read d cookie: %w", err)
	}
	password, err := a.keychainPassword()
	if err != nil {
		return monitor.Credentials{}, fmt.Errorf("get keychain password: %w", err)
	}
	xoxd, err := decryptCookie(encrypted, password)
	if err != nil {
		return monitor.Credentials{}, fmt.Errorf("decrypt d cookie: %w", err)
	}
	xoxc, err := deriveXoxc(a.httpClient, a.baseURL, xoxd)
	if err != nil {
		return monitor.Credentials{}, fmt.Errorf("derive xoxc token: %w", err)
	}
	return monitor.Credentials{XoxcToken: xoxc, XoxdToken: xoxd}, nil
}
```

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./slackauth/ -v`
Expected: PASS (composition, error propagation, and all earlier crypto/xoxc/cookie tests).

- [ ] **Step 6: Commit**

```bash
git add slackauth/keychain_darwin.go slackauth/keychain_other.go slackauth/auth.go slackauth/auth_test.go slackauth/crypto_test.go
git commit -m "feat: slackauth Authenticator composition and macOS platform glue"
```

---

## Task 9: Auto-refresh wiring in `monitor.Run`

**Files:**
- Modify: `monitor.go`
- Test: `monitor_test.go`

- [ ] **Step 1: Write the failing test**

`monitor_test.go` is `package monitor` (internal test), so reference types **unqualified** (no `monitor.` prefix). Add these imports to the existing block: `"context"`, `"fmt"`, `"time"` (keep `"strings"` and `"testing"`). Append:

```go
// fakeSlackClient lets us drive ErrTokenExpired then success.
type fakeSlackClient struct {
	convErrs []error // returned by GetDMConversations in order
	calls    int
	setCreds Credentials
	setCount int
}

func (f *fakeSlackClient) TestAuth() (string, error) { return "U_SELF", nil }
func (f *fakeSlackClient) GetDMConversations() ([]Conversation, error) {
	var err error
	if f.calls < len(f.convErrs) {
		err = f.convErrs[f.calls]
	}
	f.calls++
	return nil, err
}
func (f *fakeSlackClient) GetConversationHistory(string, string) ([]Message, error) {
	return nil, nil
}
func (f *fakeSlackClient) GetUserInfo(string) (*User, error) { return &User{}, nil }
func (f *fakeSlackClient) GetAuthenticatedUserID() string    { return "U_SELF" }
func (f *fakeSlackClient) SetCredentials(c Credentials)      { f.setCreds = c; f.setCount++ }

type fakeNotifier struct{}

func (fakeNotifier) SendNotification(string) error { return nil }

type fakeStateStore struct{}

func (fakeStateStore) Load() (*State, error) {
	return &State{LastChecked: map[string]string{}}, nil
}
func (fakeStateStore) Save(*State) error { return nil }

type fakeAuthenticator struct{ n int }

func (a *fakeAuthenticator) Authenticate() (Credentials, error) {
	a.n++
	return Credentials{XoxcToken: "xoxc-fresh", XoxdToken: "xoxd-fresh"}, nil
}

type fakeCredStore struct{ saved Credentials }

func (c *fakeCredStore) SaveCredentials(creds Credentials) error { c.saved = creds; return nil }

func TestRunRefreshesOnTokenExpired(t *testing.T) {
	client := &fakeSlackClient{
		// cycle 1: expired -> triggers refresh; cycle 2: ok.
		convErrs: []error{fmt.Errorf("wrap: %w", ErrTokenExpired), nil},
	}
	auth := &fakeAuthenticator{}
	cred := &fakeCredStore{}
	cfg := &Config{}
	cfg.Slack.PollIntervalSecs = 0 // no wait between cycles

	m := NewMonitor(client, fakeNotifier{}, fakeStateStore{}, auth, cred, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = m.Run(ctx)

	if auth.n == 0 {
		t.Error("expected Authenticate to be called on token expiry")
	}
	if client.setCount == 0 || client.setCreds.XoxcToken != "xoxc-fresh" {
		t.Errorf("expected refreshed creds pushed to client, got %+v (count %d)", client.setCreds, client.setCount)
	}
	if cred.saved.XoxcToken != "xoxc-fresh" {
		t.Errorf("expected refreshed creds persisted, got %+v", cred.saved)
	}
}
```

(This test wraps `ErrTokenExpired` with `fmt.Errorf("%w", …)` but never calls `errors.Is` itself — the `errors.Is` checks live in `monitor.go`'s `Run`/`checkAllConversations`, added in Steps 4–5. So do NOT import `errors` in the test file.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./ -run TestRunRefreshesOnTokenExpired -v`
Expected: FAIL — `Monitor` does not refresh; assertions on `auth.n`/`setCount` fail (and `Run` may block until the context timeout, which is fine).

- [ ] **Step 3: Add the refresh helper**

Add to `monitor.go`:

```go
// refreshCredentials obtains fresh credentials, pushes them into the live
// client, validates them, and persists them. It is a no-op (returning an
// error) when no authenticator is configured.
func (m *Monitor) refreshCredentials() error {
	if m.authenticator == nil {
		return fmt.Errorf("token expired but no authenticator configured")
	}
	log.Println("Refreshing Slack credentials...")
	creds, err := m.authenticator.Authenticate()
	if err != nil {
		return fmt.Errorf("re-authentication failed: %w", err)
	}
	m.slackClient.SetCredentials(creds)
	if _, err := m.slackClient.TestAuth(); err != nil {
		return fmt.Errorf("refreshed credentials failed validation: %w", err)
	}
	if m.credStore != nil {
		if err := m.credStore.SaveCredentials(creds); err != nil {
			log.Printf("Warning: failed to persist refreshed credentials: %v", err)
		}
	}
	log.Println("Slack credentials refreshed successfully")
	return nil
}
```

- [ ] **Step 4: Detect expiry at startup and in the cycle loop**

In `monitor.go`, in `Run`, replace the initial auth block:

```go
	// Validate authentication
	userID, err := m.slackClient.TestAuth()
	if err != nil {
		return err
	}
	_ = userID // Will be used for message filtering
```

with:

```go
	// Validate authentication; refresh once if the token is already expired.
	userID, err := m.slackClient.TestAuth()
	if errors.Is(err, ErrTokenExpired) {
		if rerr := m.refreshCredentials(); rerr != nil {
			return rerr
		}
		userID, err = m.slackClient.TestAuth()
	}
	if err != nil {
		return err
	}
	_ = userID // Will be used for message filtering
```

Then, in `Run`'s cycle loop, replace:

```go
		if err := m.checkAllConversations(ctx, state); err != nil {
			// Log error but continue monitoring
			log.Printf("Error checking conversations: %v", err)
		}
```

with:

```go
		if err := m.checkAllConversations(ctx, state); err != nil {
			if errors.Is(err, ErrTokenExpired) {
				if rerr := m.refreshCredentials(); rerr != nil {
					log.Printf("Token refresh failed: %v", rerr)
				}
				// Next cycle retries with refreshed (or unchanged) creds.
			} else {
				log.Printf("Error checking conversations: %v", err)
			}
		}
```

- [ ] **Step 5: Ensure cycle errors propagate `ErrTokenExpired`**

`GetDMConversations` errors already propagate from `checkAllConversations` (the first call returns up). Confirm per-conversation errors don't swallow expiry: in `checkAllConversations`, replace the per-conversation error handling:

```go
		if err := m.checkConversation(conv, state); err != nil {
			// Log error but continue checking other conversations
			continue
		}
```

with:

```go
		if err := m.checkConversation(conv, state); err != nil {
			if errors.Is(err, ErrTokenExpired) {
				return err // surface to Run so it can refresh
			}
			// Log error but continue checking other conversations
			continue
		}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./ -run TestRunRefreshesOnTokenExpired -v`
Expected: PASS. Also run `go test ./ -run TestFormatNotification -v` → PASS.

- [ ] **Step 7: Commit**

```bash
git add monitor.go monitor_test.go
git commit -m "feat: auto-refresh Slack credentials on token expiry"
```

---

## Task 10: Wire it all in `main.go`

**Files:**
- Modify: `cmd/slack-monitor/main.go`

- [ ] **Step 1: Update `loadConfig` validation (workspace required, tokens optional)**

In `cmd/slack-monitor/main.go`, in `loadConfig`, replace the token-required validation:

```go
	// Validate required fields
	if config.Slack.XoxcToken == "" {
		return nil, fmt.Errorf("slack.xoxc_token is required in config")
	}
	if config.Slack.XoxdToken == "" {
		return nil, fmt.Errorf("slack.xoxd_token is required in config")
	}
	if config.Notifications.NtfyTopic == "" {
		return nil, fmt.Errorf("notifications.ntfy_topic is required in config")
	}
```

with:

```go
	// Validate required fields. Tokens are optional: when absent they are
	// auto-derived from the macOS Slack desktop app using slack.workspace.
	if config.Slack.Workspace == "" {
		return nil, fmt.Errorf("slack.workspace is required in config (your <workspace>.slack.com subdomain)")
	}
	if config.Notifications.NtfyTopic == "" {
		return nil, fmt.Errorf("notifications.ntfy_topic is required in config")
	}
```

- [ ] **Step 2: Build the config path once and construct the new dependencies**

In `main()`, replace:

```go
	// Create implementations
	slackClient := slack.NewClient(config.Slack.XoxcToken, config.Slack.XoxdToken)
	notifier := notification.NewService(config.Notifications.NtfyTopic)
	stateStore := storage.NewFileStore()

	// Create monitor with injected dependencies
	mon := monitor.NewMonitor(slackClient, notifier, stateStore, config)
```

with:

```go
	// Create implementations
	slackClient := slack.NewClient(config.Slack.XoxcToken, config.Slack.XoxdToken)
	notifier := notification.NewService(config.Notifications.NtfyTopic)
	stateStore := storage.NewFileStore()
	authenticator := slackauth.NewAuthenticator(config.Slack.Workspace)

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home directory: %v", err)
	}
	credStore := storage.NewConfigStore(filepath.Join(home, ".slack-monitor", "config.json"))

	// If tokens are missing, derive them from the desktop app before starting.
	if config.Slack.XoxcToken == "" || config.Slack.XoxdToken == "" {
		log.Println("No tokens in config; obtaining from Slack desktop app...")
		creds, err := authenticator.Authenticate()
		if err != nil {
			log.Fatalf("Failed to obtain Slack credentials: %v\nFall back to manual token entry (see README).", err)
		}
		slackClient.SetCredentials(creds)
		if _, err := slackClient.TestAuth(); err != nil {
			log.Fatalf("Obtained credentials failed validation: %v", err)
		}
		if err := credStore.SaveCredentials(creds); err != nil {
			log.Printf("Warning: failed to cache credentials to config.json: %v", err)
		}
		log.Println("Slack credentials obtained and cached")
	}

	// Create monitor with injected dependencies
	mon := monitor.NewMonitor(slackClient, notifier, stateStore, authenticator, credStore, config)
```

- [ ] **Step 3: Add the `slackauth` import**

In `cmd/slack-monitor/main.go`, add to the import block:

```go
	"github.com/FourPalms/golang-slack-monitor/slackauth"
```

(`os` and `path/filepath` are already imported.)

- [ ] **Step 4: Build and vet the whole module**

Run: `go build ./... && go vet ./...`
Expected: PASS, no errors.

- [ ] **Step 5: Run the full test suite**

Run: `make test`
Expected: all packages PASS (`go test -v -cover ./...`).

- [ ] **Step 6: Commit**

```bash
git add cmd/slack-monitor/main.go
git commit -m "feat: wire auto-acquire and auto-refresh into main"
```

---

## Task 11: Documentation & example config

**Files:**
- Modify: `config.example.json`
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Step 1: Update `config.example.json`**

Replace its contents with:

```json
{
  "slack": {
    "workspace": "your-workspace-subdomain",
    "xoxc_token": "",
    "xoxd_token": "",
    "poll_interval_seconds": 60
  },
  "notifications": {
    "ntfy_topic": "your-ntfy-topic-with-random-suffix"
  },
  "monitor": {
    "dms_only": true
  }
}
```

- [ ] **Step 2: Update `CLAUDE.md`**

- In the "Target toolchain" line, change **Go 1.20** to **Go 1.23** (and update the CI note; README's 1.21+ is now consistent).
- In the "**Stdlib-only constraint**" paragraph, replace it with a note that the constraint is now relaxed for one dependency:

```markdown
**Near-stdlib constraint:** `go.mod` has exactly one third-party dependency —
`modernc.org/sqlite` (pure Go, no CGo) — used solely by `slackauth/` to read the
macOS Slack desktop app's Cookies database. Everything else stays stdlib
(`net/http`, `encoding/json`, hand-rolled PBKDF2, etc.). Do not add further
modules without cause.
```

- Add a bullet to the Architecture list:

```markdown
- **`slackauth/`** — implements `Authenticator`; obtains `xoxc`/`xoxd` from the
  macOS Slack desktop app (decrypts the `d` cookie via Keychain + AES, derives
  `xoxc` from the workspace page). macOS-only (`darwin` build-tagged glue).
```

- Add an invariant to the "Non-obvious invariants" list:

```markdown
6. **Token auto-refresh** (`monitor.go` `refreshCredentials`): when a SlackClient
   call returns `ErrTokenExpired`, the run loop re-authenticates via the injected
   `Authenticator`, pushes new tokens into the client, validates, and persists
   them through `CredentialStore`. `slackauth` never imports `slack`; validation
   lives in the caller. Cookie decryption params (`saltysalt`, 1003 iters,
   AES-128-CBC, `v10` prefix, optional 32-byte domain-hash prefix) are exact —
   don't tweak them.
```

- [ ] **Step 3: Update `README.md`**

- In "Step 2: Extract Slack Tokens", add a new lead section before the manual steps:

```markdown
### Step 2: Slack Tokens

**Automatic (recommended, macOS + Slack desktop app):** Set `slack.workspace`
to your `<workspace>.slack.com` subdomain and leave `xoxc_token`/`xoxd_token`
empty. On first run the app reads the `d` cookie from the Slack desktop app,
decrypts it via your Keychain (you may see a one-time access prompt), derives the
`xoxc` token from the workspace page, validates, and caches both tokens back into
`config.json`. Expired tokens are refreshed automatically while running.

**Manual (fallback):** If automatic acquisition fails (locked Keychain, Slack
changed its storage format, or you're not on macOS), extract the tokens by hand
using DevTools — see the steps below.
```

- Update the config table: add a `slack.workspace` row (string, **Yes**, the subdomain) and change `slack.xoxc_token`/`slack.xoxd_token` to **No** ("auto-derived if empty").
- In "Limitations", remove or revise "No automatic token refresh (manual re-extraction required)" to reflect that refresh is now automatic on macOS (manual fallback otherwise).

- [ ] **Step 4: Verify docs build nothing but sanity-check the example config parses**

Run: `go run ./cmd/slack-monitor 2>&1 | head -3` after temporarily pointing config at the example — OR simply verify JSON validity:
Run: `python3 -m json.tool config.example.json > /dev/null && echo OK`
Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add config.example.json CLAUDE.md README.md
git commit -m "docs: document automatic Slack token acquisition and refresh"
```

---

## Final verification

- [ ] **Run the complete suite with coverage**

Run: `make test`
Expected: all packages PASS.

- [ ] **Build the binary**

Run: `make build`
Expected: `./slack-monitor` produced, no errors.

- [ ] **Vet**

Run: `go vet ./...`
Expected: clean.

- [ ] **Manual end-to-end (macOS only, cannot run in CI):** With Slack desktop app logged in and `slack.workspace` set + tokens empty, run `./slack-monitor` and confirm it logs "Slack credentials obtained and cached" and begins monitoring. (Mark this checkbox only after a real macOS run.)
