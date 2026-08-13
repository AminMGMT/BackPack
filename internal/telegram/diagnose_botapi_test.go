package telegram

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A 401 from the Bot API means the token is not accepted, and nothing else. The
// old message — "telegram API returned status 401" — sent an operator to debug
// their relay, which is the one thing that cannot cause it: the request reached
// Telegram and Telegram answered. The error has to say which of the two it is.
func TestUnauthorizedBlamesTheTokenNotTheRelay(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body: io.NopCloser(strings.NewReader(
			`{"ok":false,"error_code":401,"description":"Unauthorized"}`)),
	}
	err := describeStatus(resp)
	if err == nil {
		t.Fatal("a 401 produced no error")
	}
	msg := err.Error()

	for _, want := range []string{"token", "401", "BotFather"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the 401 message never mentions %q: %s", want, msg)
		}
	}
	// The message must not send anyone back to the tunnel.
	if strings.Contains(strings.ToLower(msg), "tunnel is") ||
		!strings.Contains(strings.ToLower(msg), "relay is fine") {
		t.Errorf("the 401 message does not clear the relay of blame: %s", msg)
	}
}

// Telegram's own description is more specific than any status code, so it is
// quoted whenever it is there.
func TestStatusErrorQuotesTelegramsDescription(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body: io.NopCloser(strings.NewReader(
			`{"ok":false,"error_code":400,"description":"chat not found"}`)),
	}
	err := describeStatus(resp)
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("the description was dropped: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("the status was dropped: %v", err)
	}
}

// A body that is not JSON must still produce a usable error rather than a panic
// or an empty one.
func TestStatusErrorSurvivesANonJSONBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("<html>502 Bad Gateway</html>")),
	}
	err := describeStatus(resp)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("a non-JSON body lost the status: %v", err)
	}
}

// The bot token is a credential and appears in every Bot API URL, which Go
// copies into transport errors. It must never reach the screen or the journal.
func TestTokenIsRedactedFromErrors(t *testing.T) {
	const token = "123456789:AAHfakeTokenValueForTestingOnly"
	raw := `Get "https://api.telegram.org/bot` + token + `/getMe": dial tcp: i/o timeout`

	got := redactToken(raw, token)
	if strings.Contains(got, token) {
		t.Fatalf("the token survived redaction: %s", got)
	}
	if !strings.Contains(got, "<token>") {
		t.Errorf("nothing marks where the token was: %s", got)
	}
	// An empty token must not turn the message into nonsense by replacing "".
	if out := redactToken(raw, ""); out != raw {
		t.Errorf("an empty token altered the message: %s", out)
	}
}
