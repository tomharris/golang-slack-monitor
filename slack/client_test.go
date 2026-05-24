package slack

import (
	"errors"
	"testing"

	"github.com/FourPalms/golang-slack-monitor"
)

// TestNewClient tests Slack client initialization
func TestNewClient(t *testing.T) {
	xoxcToken := "test-xoxc"
	xoxdToken := "test-xoxd"

	client := NewClient(xoxcToken, xoxdToken)
	if client == nil {
		t.Error("Expected non-nil client")
	}
	if client.xoxcToken != xoxcToken {
		t.Errorf("Expected xoxc token '%s', got '%s'", xoxcToken, client.xoxcToken)
	}
	if client.xoxdToken != xoxdToken {
		t.Errorf("Expected xoxd token '%s', got '%s'", xoxdToken, client.xoxdToken)
	}
	if client.httpClient == nil {
		t.Error("Expected initialized HTTP client")
	}
}

func TestSetCredentials(t *testing.T) {
	c := NewClient("old-xoxc", "old-xoxd")
	c.SetCredentials(monitor.Credentials{XoxcToken: "new-xoxc", XoxdToken: "new-xoxd"})
	if c.xoxcToken != "new-xoxc" || c.xoxdToken != "new-xoxd" {
		t.Fatalf("SetCredentials did not update tokens: got %q/%q", c.xoxcToken, c.xoxdToken)
	}
}

func TestSlackErrorMapsToExpired(t *testing.T) {
	tests := []struct {
		apiError string
		wantExp  bool
	}{
		{"token_expired", true},
		{"invalid_auth", true},
		{"channel_not_found", false},
		{"", false},
	}
	for _, tt := range tests {
		err := slackError(tt.apiError)
		if err == nil {
			t.Fatalf("slackError(%q) = nil, want error", tt.apiError)
		}
		if got := errors.Is(err, monitor.ErrTokenExpired); got != tt.wantExp {
			t.Errorf("slackError(%q): errors.Is(ErrTokenExpired)=%v, want %v", tt.apiError, got, tt.wantExp)
		}
	}
}
