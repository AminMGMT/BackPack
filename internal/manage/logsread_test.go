package manage

import (
	"strings"
	"testing"
)

// The panel's log reads what the CLI's Live Log shows.
//
// The engine colours its level tag — "[\x1b[32mINFO\x1b[0m]" — and journalctl,
// asked for a line holding a control character with no terminal attached,
// prints "[63B blob data]" in its place unless it is given --all. That is
// systemd up to 254, which is what Ubuntu 20.04 and 22.04 run: every line in
// the panel's Logs dialog was a blob while the same journal in the CLI (a
// terminal) read fine. So the read asks for everything, and the colour codes
// are taken out before the text leaves this package.
func TestLogsAreReadWholeAndWithoutColour(t *testing.T) {
	var gotArgs []string
	was := journalctl
	journalctl = func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("2026-09-29T17:59:21+03:30 host backpack[12]: 29-Sep 17:59:21 [\x1b[32mINFO\x1b[0m] control channel up\n"), nil
	}
	t.Cleanup(func() { journalctl = was })

	out := readLogs("de-frankfurt", 50)
	if !contains(gotArgs, "--all") {
		t.Errorf("journalctl args %q lack --all, so an older systemd prints [blob data]", gotArgs)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("colour codes reached the reader: %q", out)
	}
	if !strings.Contains(out, "[INFO] control channel up") {
		t.Errorf("the line was not kept: %q", out)
	}
}
