//go:build !darwin

package slackauth

import "errors"

// ErrUnsupportedPlatform indicates automatic token acquisition is macOS-only.
var ErrUnsupportedPlatform = errors.New("automatic Slack token acquisition is only supported on macOS")

func keychainPassword() (string, error) { return "", ErrUnsupportedPlatform }
func defaultCookiePath() (string, error) { return "", ErrUnsupportedPlatform }
