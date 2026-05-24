package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfig tests config loading and validation
func TestLoadConfig(t *testing.T) {
	// Create temp directory for test config
	tmpDir := t.TempDir()

	// Save original HOME and restore after test
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tmpDir)

	// Test 1: Missing config file
	_, err := loadConfig()
	if err == nil {
		t.Error("Expected error for missing config file")
	}

	// Test 2: Valid config
	validConfig := map[string]interface{}{
		"slack": map[string]interface{}{
			"workspace":             "acme",
			"xoxc_token":            "test-xoxc",
			"xoxd_token":            "test-xoxd",
			"poll_interval_seconds": 30,
		},
		"notifications": map[string]interface{}{
			"ntfy_topic": "test-topic",
		},
		"monitor": map[string]interface{}{
			"dms_only": true,
		},
	}

	// Create .slack-monitor directory
	monitorDir := filepath.Join(tmpDir, ".slack-monitor")
	if err := os.MkdirAll(monitorDir, 0700); err != nil {
		t.Fatalf("Failed to create monitor dir: %v", err)
	}

	configPath := filepath.Join(monitorDir, "config.json")
	data, _ := json.Marshal(validConfig)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Errorf("Expected no error for valid config, got: %v", err)
	}
	if config.Slack.XoxcToken != "test-xoxc" {
		t.Errorf("Expected xoxc_token 'test-xoxc', got '%s'", config.Slack.XoxcToken)
	}
	if config.Slack.PollIntervalSecs != 30 {
		t.Errorf("Expected poll interval 30, got %d", config.Slack.PollIntervalSecs)
	}

	// Test 3: Missing required field. Tokens are now optional (auto-derived),
	// but slack.workspace is required.
	invalidConfig := map[string]interface{}{
		"slack": map[string]interface{}{
			"xoxc_token": "test-xoxc",
			"xoxd_token": "test-xoxd",
			// Missing workspace
		},
		"notifications": map[string]interface{}{
			"ntfy_topic": "test-topic",
		},
	}
	data, _ = json.Marshal(invalidConfig)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	_, err = loadConfig()
	if err == nil {
		t.Error("Expected error for missing slack.workspace")
	}
}

// TestConfigDefaults tests that default values are applied correctly
func TestConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tmpDir)

	// Config without optional fields
	minimalConfig := map[string]interface{}{
		"slack": map[string]interface{}{
			"workspace":  "acme",
			"xoxc_token": "test-xoxc",
			"xoxd_token": "test-xoxd",
		},
		"notifications": map[string]interface{}{
			"ntfy_topic": "test-topic",
		},
	}

	monitorDir := filepath.Join(tmpDir, ".slack-monitor")
	if err := os.MkdirAll(monitorDir, 0700); err != nil {
		t.Fatalf("Failed to create monitor dir: %v", err)
	}

	configPath := filepath.Join(monitorDir, "config.json")
	data, _ := json.Marshal(minimalConfig)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify defaults
	if config.Slack.PollIntervalSecs != defaultPollIntervalSecs {
		t.Errorf("Expected default poll interval %d, got %d", defaultPollIntervalSecs, config.Slack.PollIntervalSecs)
	}
	if config.Monitor.DMsOnly != defaultDMsOnly {
		t.Errorf("Expected default DMsOnly %v, got %v", defaultDMsOnly, config.Monitor.DMsOnly)
	}
}
