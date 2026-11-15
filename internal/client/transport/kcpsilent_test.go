package transport

import (
	"strings"
	"testing"
)

// A silent server names what the firewall has to let in, per carrier.
func TestASilentServerNamesWhatToLetIn(t *testing.T) {
	for _, c := range []struct {
		cfg  KcpConfig
		want string
	}{
		{KcpConfig{}, "UDP port 9000"},
		{KcpConfig{UseICMP: true}, "ICMP echo"},
		{KcpConfig{UsePck: true}, "TCP port 9000"},
	} {
		cfg := c.cfg
		tr := &KcpTransport{config: &cfg}
		got := tr.silentServer("203.0.113.5:9000")
		if !strings.Contains(got, c.want) || !strings.Contains(got, "203.0.113.5:9000") {
			t.Errorf("%+v: %q", c.cfg, got)
		}
	}
}
