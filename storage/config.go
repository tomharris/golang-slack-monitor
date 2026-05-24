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
