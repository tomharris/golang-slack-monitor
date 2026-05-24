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
