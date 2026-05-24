package slack

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FourPalms/golang-slack-monitor"
)

const (
	conversationLimit = 500 // Max conversations to fetch per API call
	messageLimit      = 100 // Max messages to fetch per API call
)

// Client implements the monitor.SlackClient interface
type Client struct {
	xoxcToken           string
	xoxdToken           string
	httpClient          *http.Client
	authenticatedUserID string // ID of the authenticated user (to filter own messages)
}

// NewClient creates a new Slack API client
func NewClient(xoxcToken, xoxdToken string) *Client {
	return &Client{
		xoxcToken: xoxcToken,
		xoxdToken: xoxdToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// TestAuth validates the authentication tokens and returns the authenticated user ID
func (c *Client) TestAuth() (string, error) {
	// slack-go uses POST with token as a parameter for auth.test
	params := url.Values{
		"token": {c.xoxcToken},
	}
	body, err := c.makeRequest("POST", "auth.test", params)
	if err != nil {
		return "", err
	}

	var response authTestResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse auth response: %w", err)
	}

	if !response.OK {
		return "", slackError(response.Error)
	}

	log.Printf("Authenticated as %s (%s) in workspace %s", response.User, response.UserID, response.Team)
	c.authenticatedUserID = response.UserID
	return response.UserID, nil
}

// GetDMConversations fetches all DM conversations
func (c *Client) GetDMConversations() ([]monitor.Conversation, error) {
	params := url.Values{}
	params.Set("types", "im")
	params.Set("exclude_archived", "true")
	params.Set("limit", fmt.Sprintf("%d", conversationLimit))
	params.Set("token", c.xoxcToken) // GET requests need token as query parameter

	body, err := c.makeRequest("GET", "conversations.list", params)
	if err != nil {
		return nil, err
	}

	var response conversationsListResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse conversations response: %w", err)
	}

	if !response.OK {
		return nil, slackError(response.Error)
	}

	// Convert API response to domain types
	conversations := make([]monitor.Conversation, len(response.Channels))
	for i, ch := range response.Channels {
		conversations[i] = monitor.Conversation{
			ID:            ch.ID,
			User:          ch.User,
			IsUserDeleted: ch.IsUserDeleted,
		}
	}

	return conversations, nil
}

// GetConversationHistory fetches messages from a conversation since a given timestamp
func (c *Client) GetConversationHistory(channelID, oldestTS string) ([]monitor.Message, error) {
	params := url.Values{}
	params.Set("channel", channelID)
	if oldestTS != "" {
		params.Set("oldest", oldestTS)
	}
	params.Set("limit", fmt.Sprintf("%d", messageLimit))
	params.Set("token", c.xoxcToken) // GET requests need token as query parameter

	body, err := c.makeRequest("GET", "conversations.history", params)
	if err != nil {
		return nil, err
	}

	var response conversationsHistoryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse history response: %w", err)
	}

	if !response.OK {
		return nil, slackError(response.Error)
	}

	// Convert API response to domain types
	messages := make([]monitor.Message, len(response.Messages))
	for i, msg := range response.Messages {
		messages[i] = monitor.Message{
			Timestamp: msg.Timestamp,
			User:      msg.User,
			Text:      msg.Text,
			Type:      msg.Type,
		}
	}

	return messages, nil
}

// GetUserInfo fetches information about a user
func (c *Client) GetUserInfo(userID string) (*monitor.User, error) {
	params := url.Values{}
	params.Set("user", userID)
	params.Set("token", c.xoxcToken) // GET requests need token as query parameter

	body, err := c.makeRequest("GET", "users.info", params)
	if err != nil {
		return nil, err
	}

	var response usersInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse user response: %w", err)
	}

	if !response.OK {
		return nil, slackError(response.Error)
	}

	// Convert API response to domain type
	return &monitor.User{
		ID:       response.User.ID,
		Name:     response.User.Name,
		RealName: response.User.RealName,
	}, nil
}

// GetAuthenticatedUserID returns the ID of the authenticated user
func (c *Client) GetAuthenticatedUserID() string {
	return c.authenticatedUserID
}

// SetCredentials replaces the client's auth tokens (used on refresh).
func (c *Client) SetCredentials(creds monitor.Credentials) {
	c.xoxcToken = creds.XoxcToken
	c.xoxdToken = creds.XoxdToken
}

// slackError converts a Slack API error string into an error, mapping
// session-expiry errors to monitor.ErrTokenExpired so the core can react.
func slackError(apiError string) error {
	switch apiError {
	case "token_expired", "invalid_auth":
		return fmt.Errorf("%w (%s)", monitor.ErrTokenExpired, apiError)
	default:
		return fmt.Errorf("Slack API error: %s", apiError)
	}
}

// makeRequest makes an authenticated request to the Slack API
func (c *Client) makeRequest(method, endpoint string, params url.Values) ([]byte, error) {
	apiURL := "https://slack.com/api/" + endpoint

	var req *http.Request
	var err error

	if method == "GET" {
		if len(params) > 0 {
			apiURL += "?" + params.Encode()
		}
		req, err = http.NewRequest("GET", apiURL, nil)
	} else {
		req, err = http.NewRequest("POST", apiURL, strings.NewReader(params.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication - stealth mode uses both tokens AND two cookies
	// Slack requires both "d" and "d-s" cookies (discovered from rusq/slackdump library)
	// CRITICAL: xoxd goes in "d" cookie, xoxc goes in token parameter (not the other way around!)
	dCookie := &http.Cookie{
		Name:  "d",
		Value: c.xoxdToken, // xoxd token goes in "d" cookie
	}
	req.AddCookie(dCookie)

	// d-s cookie is a timestamp (current Unix time - 10 seconds)
	dsCookie := &http.Cookie{
		Name:  "d-s",
		Value: fmt.Sprintf("%d", time.Now().Unix()-10),
	}
	req.AddCookie(dsCookie)

	// For POST requests, token is in the body parameters (not Authorization header)
	// For GET requests, token MUST be added as a query parameter by the caller
	// Authorization header is NOT used with stealth mode cookies

	// Add browser User-Agent to match slack-mcp-server
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
