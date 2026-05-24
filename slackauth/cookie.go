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
