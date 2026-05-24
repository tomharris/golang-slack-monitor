package slackauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticate_Composition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bootHTML)) // from xoxc_test.go
	}))
	defer srv.Close()

	a := &Authenticator{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
		readEncrypted: func() ([]byte, error) {
			// "v10" + AES-CBC of url-encoded xoxd, password "peanuts".
			return makeEncryptedCookie(t, "peanuts", "xoxd-test%2Dvalue"), nil
		},
		keychainPassword: func() (string, error) { return "peanuts", nil },
	}

	creds, err := a.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if creds.XoxcToken != "xoxc-real-1234-5678" {
		t.Errorf("xoxc = %q", creds.XoxcToken)
	}
	if creds.XoxdToken != "xoxd-test-value" {
		t.Errorf("xoxd = %q", creds.XoxdToken)
	}
}

func TestAuthenticate_PropagatesCookieError(t *testing.T) {
	sentinel := errors.New("keyring locked")
	a := &Authenticator{
		baseURL:          "http://unused",
		httpClient:       http.DefaultClient,
		readEncrypted:    func() ([]byte, error) { return nil, sentinel },
		keychainPassword: func() (string, error) { return "peanuts", nil },
	}
	if _, err := a.Authenticate(); !errors.Is(err, sentinel) {
		t.Errorf("Authenticate err = %v, want wrap of sentinel", err)
	}
}
