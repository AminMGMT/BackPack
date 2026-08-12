package manage

import (
	"os/exec"
	"strconv"

	"github.com/backpack/backpack/internal/app"
)

// Logs returns the last n journal lines for a tunnel's service.
//
// It lives here rather than in the web panel because the Telegram bot needs the
// same thing, and the panel imports the bot — so the bot cannot reach back for
// it. Both now ask the same question in the same place, which is also the only
// way "the logs" means one thing across the two interfaces.
func Logs(name string, n int) string {
	if n <= 0 {
		n = 100
	}
	out, err := exec.Command("journalctl",
		"-u", app.ServiceName(name),
		"-n", strconv.Itoa(n),
		"--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "No logs available for " + name
	}
	return string(out)
}
