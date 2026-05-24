package slackauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// extractAPIToken finds the value of the "api_token" key in the workspace
// boot HTML. It keys on "api_token" (not a bare xoxc- match) so decoy
// placeholders elsewhere in the page are ignored, then JSON-decodes the
// string value so escapes are handled correctly.
func extractAPIToken(html string) (string, error) {
	const key = `"api_token"`
	idx := strings.Index(html, key)
	if idx < 0 {
		return "", fmt.Errorf("api_token not found in workspace page")
	}
	rest := html[idx+len(key):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return "", fmt.Errorf("malformed api_token entry")
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if len(rest) == 0 || rest[0] != '"' {
		return "", fmt.Errorf("api_token value is not a string")
	}
	// Find the closing quote of the JSON string (skipping escaped quotes).
	end := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\\' {
			i++
			continue
		}
		if rest[i] == '"' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("unterminated api_token string")
	}
	var token string
	if err := json.Unmarshal([]byte(rest[:end+1]), &token); err != nil {
		return "", fmt.Errorf("decode api_token: %w", err)
	}
	if !strings.HasPrefix(token, "xoxc-") {
		return "", fmt.Errorf("api_token value is not an xoxc token")
	}
	return token, nil
}

// deriveXoxc fetches the workspace base URL with the d cookie and returns the
// embedded xoxc api_token. baseURL is e.g. https://acme.slack.com.
func deriveXoxc(client *http.Client, baseURL, xoxdCookie string) (string, error) {
	req, err := http.NewRequest("GET", baseURL+"/", nil)
	if err != nil {
		return "", fmt.Errorf("build workspace request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "d", Value: xoxdCookie})
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch workspace page: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read workspace page: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("workspace page returned status %d", resp.StatusCode)
	}
	return extractAPIToken(string(body))
}
