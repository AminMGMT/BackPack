package manage

import (
	"bytes"
	"strings"
	"testing"
)

func TestConnectionTestStartupKeepsSkippedReasonsAndCoverScope(t *testing.T) {
	s := &ConnTestIran{link: ConnTestLink{Host: "203.0.113.10", Preset: PresetTurbo},
		visionTarget: "www.apple.com:443", visionLog: "/tmp/private-test/vision-cover.log",
		cases: []*connTestCase{
			{kind: "reverse", tr: "tcp"}, {kind: "reverse", tr: "vision"},
			{kind: "reverse", tr: "naive", skip: "sing-box helper is not installed"},
		}}
	var out bytes.Buffer
	ctMenuStage(&out, "1", "Preparing test tunnels", "Checking listeners and covers.")
	ctMenuStartup(&out, s)
	for _, want := range []string{"2 configured · 1 skipped", "www.apple.com:443", "two-server test starts after Kharej joins",
		"Apple fallback", "sing-box helper is not installed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing startup information %q:\n%s", want, out.String())
		}
	}
	s.visionTarget = "www.bing.com:443"
	out.Reset()
	ctMenuStartup(&out, s)
	if strings.Contains(out.String(), "Apple fallback") {
		t.Error("Apple warning was shown for another cover")
	}
	s.visionTarget = ""
	s.cases[1].skip = "no compatible Vision cover"
	out.Reset()
	ctMenuStartup(&out, s)
	if !strings.Contains(out.String(), s.cases[1].skip) || !strings.Contains(out.String(), s.visionLog) {
		t.Error("failed Vision cover lost its reason or diagnostic path")
	}
}
