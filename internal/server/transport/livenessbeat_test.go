package transport

import (
	"os"
	"regexp"
	"testing"
	"time"
)

func TestTheControlHeartbeatIsNeverSlowerThanTenSeconds(t *testing.T) {
	for configured, want := range map[time.Duration]time.Duration{
		0:                maxLivenessBeat,
		5 * time.Second:  5 * time.Second,
		10 * time.Second: 10 * time.Second,
		40 * time.Second: maxLivenessBeat,
	} {
		if got := livenessBeat(configured); got != want {
			t.Errorf("livenessBeat(%s) = %s, want %s", configured, got, want)
		}
	}
}

// Every server transport's control channel beats through livenessBeat. A
// transport that ticks on the raw setting is one a crashed server leaves its
// client waiting on for 112 seconds.
func TestEveryControlChannelUsesTheLivenessBeat(t *testing.T) {
	raw := regexp.MustCompile(`func \(s \*\w+\) channelHandler[^{]*\{(?s:.*?)time\.NewTicker\(([^\n]*)\)\n`)
	for _, f := range []string{"tcp.go", "tcpmux.go", "ws.go", "wsmux.go", "kcp.go", "quic.go", "udp.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := raw.FindSubmatch(src)
		if m == nil {
			t.Errorf("%s: no heartbeat ticker found in channelHandler — the reader is out of date", f)
			continue
		}
		if string(m[1]) != "livenessBeat(s.config.Heartbeat)" {
			t.Errorf("%s: the control heartbeat ticks on %s, not livenessBeat", f, m[1])
		}
	}
}
