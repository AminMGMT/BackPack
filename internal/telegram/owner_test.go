package telegram

import (
	"strings"
	"testing"
)

// The panel password reaches the bot's owner only. A second admin with write
// access runs the tunnels; the password — and a backup, which carries it — is
// everything, including who else gets in.
func TestOnlyTheOwnerSeesThePanelPassword(t *testing.T) {
	c := Config{AdminID: "1", Admins: []Admin{{ID: "2"}, {ID: "3", ReadOnly: true}}}
	for id, want := range map[string]bool{"1": true, "2": false, "3": false, "9": false} {
		if got := c.isOwner(id); got != want {
			t.Errorf("isOwner(%s) = %v, want %v", id, got, want)
		}
	}
	for _, id := range []int64{2, 3} {
		r := route(c, tgUser{ID: id}, "nav:webui")
		if !strings.Contains(r.toast, "owner") {
			t.Errorf("admin %d reached the Web Panel screen (toast %q)", id, r.toast)
		}
		r = route(c, tgUser{ID: id}, "act:backup")
		if id == 2 && !strings.Contains(r.toast, "owner") {
			t.Errorf("write admin %d was offered a backup (toast %q)", id, r.toast)
		}
	}
}
