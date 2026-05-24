package monitor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// Message represents a message in a Slack conversation
type Message struct {
	Timestamp string // Slack message timestamp (unique ID)
	User      string // User ID who sent the message
	Text      string // Message text content
	Type      string // Message type (e.g., "message")
}

// Conversation represents a Slack DM conversation
type Conversation struct {
	ID            string // Channel ID (e.g., "D06...")
	User          string // Other user's ID in the DM
	IsUserDeleted bool   // Whether the user has been deleted
}

// State represents the monitoring state - tracks last checked timestamp per conversation
type State struct {
	LastChecked map[string]string // channel_id -> timestamp
}

// User represents a Slack user
type User struct {
	ID       string
	Name     string
	RealName string
}

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

// Config represents the application configuration
type Config struct {
	Slack struct {
		XoxcToken        string `json:"xoxc_token"`
		XoxdToken        string `json:"xoxd_token"`
		Workspace        string `json:"workspace"`
		WorkspaceID      string `json:"workspace_id"`
		PollIntervalSecs int    `json:"poll_interval_seconds"`
	} `json:"slack"`
	Notifications struct {
		NtfyTopic string `json:"ntfy_topic"`
	} `json:"notifications"`
	Monitor struct {
		DMsOnly bool `json:"dms_only"`
	} `json:"monitor"`
}

// SlackClient defines the interface for Slack API operations
type SlackClient interface {
	// TestAuth validates authentication and returns the authenticated user ID
	TestAuth() (string, error)

	// GetDMConversations returns all DM conversations
	GetDMConversations() ([]Conversation, error)

	// GetConversationHistory fetches messages since the given timestamp
	GetConversationHistory(channelID, oldestTS string) ([]Message, error)

	// GetUserInfo fetches information about a user
	GetUserInfo(userID string) (*User, error)

	// GetAuthenticatedUserID returns the ID of the authenticated user
	GetAuthenticatedUserID() string

	// SetCredentials replaces the client's auth tokens (used on refresh)
	SetCredentials(Credentials)
}

// Notifier defines the interface for sending notifications
type Notifier interface {
	// SendNotification sends a notification message
	SendNotification(message string) error
}

// StateStore defines the interface for state persistence
type StateStore interface {
	// Load loads the state from storage
	Load() (*State, error)

	// Save persists the state to storage
	Save(state *State) error
}

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

// Run starts the monitoring loop
func (m *Monitor) Run(ctx context.Context) error {
	// Validate authentication
	userID, err := m.slackClient.TestAuth()
	if err != nil {
		return err
	}
	_ = userID // Will be used for message filtering

	// Load state
	state, err := m.stateStore.Load()
	if err != nil {
		return err
	}

	pollInterval := time.Duration(m.config.Slack.PollIntervalSecs) * time.Second
	log.Println("Starting monitoring...")

	// Use check-then-wait pattern to prevent overlapping cycles
	for {
		// Check for cancellation before starting cycle
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Run check cycle
		log.Println("Checking for new messages...")
		cycleStart := time.Now()
		if err := m.checkAllConversations(ctx, state); err != nil {
			// Log error but continue monitoring
			log.Printf("Error checking conversations: %v", err)
		}
		cycleDuration := time.Since(cycleStart)

		log.Printf("Check cycle completed in %dms, waiting %ds before next cycle", cycleDuration.Milliseconds(), int(pollInterval.Seconds()))

		// Wait for configured interval AFTER check completes
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
			// Next cycle will start
		}
	}
}

// checkAllConversations checks all DM conversations for new messages
func (m *Monitor) checkAllConversations(ctx context.Context, state *State) error {
	// Get all DM conversations
	conversations, err := m.slackClient.GetDMConversations()
	if err != nil {
		return err
	}

	log.Printf("Checking %d DM conversation(s)", len(conversations))

	// Log deleted users with display names
	var deletedUsers []struct {
		channelID   string
		userID      string
		displayName string
	}
	for _, conv := range conversations {
		if conv.IsUserDeleted {
			displayName := m.getUserDisplayName(conv.User)
			deletedUsers = append(deletedUsers, struct {
				channelID   string
				userID      string
				displayName string
			}{conv.ID, conv.User, displayName})
		}
	}

	if len(deletedUsers) > 0 {
		log.Printf("Found %d conversation(s) with deleted users (%.1f%% of total)",
			len(deletedUsers), float64(len(deletedUsers))/float64(len(conversations))*100)
		log.Printf("Complete list of deleted user conversations:")
		for _, du := range deletedUsers {
			log.Printf("  - %s: %s (user ID: %s)", du.channelID, du.displayName, du.userID)
		}
		log.Printf("Skipping deleted user conversations (they cannot send new messages)")
	}

	// Filter to only active conversations (skip deleted users)
	activeConversations := make([]Conversation, 0, len(conversations))
	for _, conv := range conversations {
		if !conv.IsUserDeleted {
			activeConversations = append(activeConversations, conv)
		}
	}

	log.Printf("Monitoring %d active conversation(s) (skipped %d deleted)", len(activeConversations), len(deletedUsers))

	// Check each active conversation for new messages
	for _, conv := range activeConversations {
		// Check for cancellation before each conversation
		select {
		case <-ctx.Done():
			return nil
		default:
			// Continue processing
		}

		if err := m.checkConversation(conv, state); err != nil {
			// Log error but continue checking other conversations
			continue
		}
	}

	// Save state after each check cycle
	if err := m.stateStore.Save(state); err != nil {
		return err
	}
	log.Printf("State saved (%d conversations tracked)", len(state.LastChecked))
	return nil
}

// checkConversation checks a single conversation for new messages
func (m *Monitor) checkConversation(conv Conversation, state *State) error {
	// Get display name for logging
	displayName := m.getUserDisplayName(conv.User)
	log.Printf("  → Checking DM with %s (%s)", displayName, conv.ID)

	// Get last checked timestamp for this conversation
	lastChecked, exists := state.LastChecked[conv.ID]
	if !exists {
		// First time checking this conversation, start from now to avoid backlog spam
		lastChecked = formatTimestamp(time.Now())
		state.LastChecked[conv.ID] = lastChecked
	}

	// Fetch messages since last check
	messages, err := m.slackClient.GetConversationHistory(conv.ID, lastChecked)
	if err != nil {
		return err
	}

	// Process messages in reverse order (oldest first)
	newCount := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]

		// Skip non-user messages and our own messages
		if msg.User == "" || msg.Type != "message" || msg.User == m.slackClient.GetAuthenticatedUserID() {
			continue
		}

		// Get user display name and format notification
		displayName := m.getUserDisplayName(msg.User)
		notificationMsg := formatNotification(displayName, msg.Text)

		// Send notification
		if err := m.notifier.SendNotification(notificationMsg); err != nil {
			// Log error but continue processing
			_ = err
		}

		newCount++

		// Update last checked to this message's timestamp
		state.LastChecked[conv.ID] = msg.Timestamp
	}

	// Note: If newCount == 0, we intentionally do NOT update state.LastChecked
	// Preserving the actual timestamp allows tiered monitoring to work correctly

	return nil
}

// formatTimestamp formats a time.Time as a Slack timestamp
func formatTimestamp(t time.Time) string {
	return formatFloat(float64(t.Unix()))
}

// formatFloat formats a float with 6 decimal places (Slack timestamp format)
func formatFloat(f float64) string {
	return fmt.Sprintf("%.6f", f)
}

// formatNotification formats a message for notification
func formatNotification(userName, messageText string) string {
	const maxLength = 500
	if len(messageText) > maxLength {
		messageText = messageText[:maxLength-3] + "..."
	}
	return fmt.Sprintf("DM from %s: %s", userName, messageText)
}

// getUserDisplayName gets a user's display name (from cache or API)
func (m *Monitor) getUserDisplayName(userID string) string {
	// Check cache first
	if displayName, exists := m.userCache[userID]; exists {
		return displayName
	}

	// Fetch from API
	user, err := m.slackClient.GetUserInfo(userID)
	if err != nil {
		// Fallback to user ID on error
		return userID
	}

	// Determine display name
	displayName := user.RealName
	if displayName == "" {
		displayName = user.Name
	}
	if displayName == "" {
		displayName = userID // Final fallback
	}

	// Cache for future use
	m.userCache[userID] = displayName
	return displayName
}
