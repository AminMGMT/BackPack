package telegram

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("the relay is down")
}

// A failed request never carries the bot token out in its error.
//
// Go's HTTP client puts the whole URL in the error it returns, and the Bot API
// puts the token in the URL. postMessage returned that error as it was, and
// the panel's "send a test message" shows its caller the error — so a
// write-scoped token that could make the send fail (by stopping the relay)
// could read the bot token the panel otherwise masks.
func TestAFailedSendDoesNotRevealTheBotToken(t *testing.T) {
	const token = "123456:AAE-the-bot-token-itself"
	err := postMessage(&http.Client{Transport: failingTransport{}}, token, "1", "hi", "")
	if err == nil {
		t.Fatal("setup: the send did not fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the error carries the bot token: %v", err)
	}
}
