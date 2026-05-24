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
