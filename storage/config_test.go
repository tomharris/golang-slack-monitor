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
