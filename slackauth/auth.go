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
	baseURL          string // e.g. https://acme.slack.com
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
