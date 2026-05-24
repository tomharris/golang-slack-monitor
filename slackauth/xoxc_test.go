package slackauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const bootHTML = `<html><body>
<script>var decoy = "xoxc-DECOY-NOT-THE-TOKEN";</script>
<script>boot_data = {"team_id":"T123","api_token":"xoxc-real-1234-5678","other":"x"};</script>
</body></html>`

func TestExtractAPIToken_PrefersKeyedValue(t *testing.T) {
	got, err := extractAPIToken(bootHTML)
	if err != nil {
		t.Fatalf("extractAPIToken: %v", err)
	}
	if got != "xoxc-real-1234-5678" {
		t.Errorf("extractAPIToken = %q, want xoxc-real-1234-5678 (decoy must be ignored)", got)
	}
}

func TestExtractAPIToken_NotFound(t *testing.T) {
	if _, err := extractAPIToken("<html>no token here</html>"); err == nil {
		t.Error("expected error when api_token absent, got nil")
	}
}

func TestDeriveXoxc_SendsCookieAndExtracts(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("d"); err == nil {
			gotCookie = c.Value
		}
		w.Write([]byte(bootHTML))
	}))
	defer srv.Close()

	token, err := deriveXoxc(srv.Client(), srv.URL, "xoxd-my-cookie")
	if err != nil {
		t.Fatalf("deriveXoxc: %v", err)
	}
	if token != "xoxc-real-1234-5678" {
		t.Errorf("deriveXoxc token = %q", token)
	}
	if gotCookie != "xoxd-my-cookie" {
		t.Errorf("d cookie not sent: got %q", gotCookie)
	}
}
