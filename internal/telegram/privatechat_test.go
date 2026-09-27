package telegram

import (
	"encoding/json"
	"testing"
)

// The bot answers in the chat a request came from. In a group that would post
// the secret screens — the panel password, the owner's backup — to everyone in
// it, so only private chats are answered.
func TestOnlyPrivateChatsAreAnswered(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"chat":{"id":42,"type":"private"}}`:         true,
		`{"chat":{"id":42}}`:                          false,
		`{"chat":{"id":-100123,"type":"supergroup"}}`: false,
		`{"chat":{"id":-42,"type":"group"}}`:          false,
		`{"chat":{"id":-100999,"type":"channel"}}`:    false,
	} {
		var m tgMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		if got := m.privateChat(); got != want {
			t.Errorf("%s: privateChat() = %v, want %v", raw, got, want)
		}
	}
}
