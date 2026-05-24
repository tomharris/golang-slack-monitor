package monitor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestFormatNotification tests message formatting for notifications
func TestFormatNotification(t *testing.T) {
	tests := []struct {
		userName string
		message  string
		expected string
	}{
		{
			userName: "John Doe",
			message:  "Hello world",
			expected: "DM from John Doe: Hello world",
		},
		{
			userName: "Jane",
			message:  "This is a very long message that exceeds the 100 character limit and should be truncated properly with ellipsis at the end to make it fit",
			expected: "DM from Jane: This is a very long message that exceeds the 100 character limit and should be truncated properly with ellipsis at the end to make it fit",
		},
		{
			userName: "Bob",
			message:  strings.Repeat("a", 600), // 600 chars exceeds 500 limit
			expected: "DM from Bob: " + strings.Repeat("a", 497) + "...",
		},
		{
			userName: "Bot",
			message:  "",
			expected: "DM from Bot: ",
		},
	}

	for _, tt := range tests {
		result := formatNotification(tt.userName, tt.message)
		if result != tt.expected {
			t.Errorf("formatNotification(%q, %q) = %q, want %q", tt.userName, tt.message, result, tt.expected)
		}
	}
}

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
