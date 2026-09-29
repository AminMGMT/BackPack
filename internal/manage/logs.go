package manage

import (
	"os/exec"
	"regexp"
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
	// Shared and briefly cached, because the panel's log drawer polls this on a
	// two-second timer and every caller used to get its own journalctl. See
	// logscache.go for what that did to journald.
	return journalCache.Get(name+"\x00"+strconv.Itoa(n), func() string {
		return readLogs(name, n)
	})
}

// journalctl runs journalctl; a variable so a test can answer instead.
var journalctl = func(args ...string) ([]byte, error) {
	return exec.Command("journalctl", args...).CombinedOutput()
}

// ansiEscape matches the colour codes the engine's text log wraps its level in.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// readLogs is the uncached read, and the only place that runs journalctl.
//
// --all, because the engine colours its level tag and journalctl up to systemd
// 254 — Ubuntu 20.04 and 22.04 — prints "[63B blob data]" for any line holding
// a control character when no terminal is attached. Every line in the panel's
// log and the bot's was a blob there, while the CLI's Live Log, which runs in
// a terminal, read the same journal fine. The codes are then taken out, since
// neither a browser nor Telegram draws them.
func readLogs(name string, n int) string {
	out, err := journalctl(
		"-u", app.ServiceName(name),
		"-n", strconv.Itoa(n),
		"--no-pager", "--all", "-o", "short-iso")
	if err != nil && len(out) == 0 {
		return "No logs available for " + name
	}
	return ansiEscape.ReplaceAllString(string(out), "")
}
