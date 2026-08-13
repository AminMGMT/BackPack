package manage

import "testing"

// parsePing has to survive the two shapes ping prints: a healthy summary, and a
// 100%-loss run with no rtt line at all.
func TestParsePing(t *testing.T) {
	healthy := `PING 1.1.1.1 (1.1.1.1) 56(84) bytes of data.

--- 1.1.1.1 ping statistics ---
25 packets transmitted, 25 received, 0% packet loss, time 4823ms
rtt min/avg/max/mdev = 41.2/43.8/58.1/3.1 ms`
	g := parsePing(healthy)
	if !g.OK || g.AvgMS != 43.8 {
		t.Fatalf("avg = %v ok = %v, want 43.8 true", g.AvgMS, g.OK)
	}
	if g.LossPct != 0 {
		t.Fatalf("loss = %v, want 0", g.LossPct)
	}

	dead := `PING 10.0.0.1 (10.0.0.1) 56(84) bytes of data.

--- 10.0.0.1 ping statistics ---
25 packets transmitted, 0 received, 100% packet loss, time 24560ms`
	g = parsePing(dead)
	if g.OK {
		t.Fatalf("a no-reply run must not report OK: %+v", g)
	}
	if g.LossPct != 100 {
		t.Fatalf("loss = %v, want 100", g.LossPct)
	}
}

// The rating thresholds are what a player reads a number against, so pin the
// boundaries: a hair under 60 is still good, exactly 180 is bad.
func TestRateEstimatedPing(t *testing.T) {
	cases := []struct {
		ms       int
		label    string
		severity int
	}{
		{45, "excellent", 0},
		{59, "excellent", 0},
		{75, "good", 0},
		{110, "playable", 1},
		{160, "rough", 1},
		{180, "bad", 2},
		{300, "bad", 2},
	}
	for _, c := range cases {
		r := RateEstimatedPing(c.ms)
		if r.Label != c.label || r.Severity != c.severity {
			t.Errorf("%dms => %q/%d, want %q/%d", c.ms, r.Label, r.Severity, c.label, c.severity)
		}
	}
}

// The whole point of RecommendFEC is that parity rises with loss and always
// clears the loss it was chosen for, since loss comes in bursts.
func TestRecommendFECRisesWithLoss(t *testing.T) {
	q := func(lossPct float64) PathQuality {
		// 100 sent, so received sets the loss percentage directly, and >=2
		// received keeps it Usable.
		return PathQuality{Sent: 100, Received: 100 - int(lossPct)}
	}
	cases := []struct {
		loss   float64
		parity int
	}{
		{0.5, 5},
		{3, 5},
		{10, 8},
		{25, 8},
	}
	var lastCover float64 = -1
	for _, c := range cases {
		p := RecommendFEC(q(c.loss))
		if p.Parity != c.parity {
			t.Errorf("loss %.1f%% => parity %d, want %d", c.loss, p.Parity, c.parity)
		}
		if !p.Set() {
			t.Errorf("loss %.1f%% => no FEC, the gaming transport must always repair", c.loss)
		}
		cover := float64(p.Parity) / float64(p.Data+p.Parity) * 100
		if cover < c.loss {
			t.Errorf("loss %.1f%% => %s covers only %.0f%%, must exceed the loss", c.loss, p.Ratio(), cover)
		}
		if cover < lastCover {
			t.Errorf("coverage went backwards as loss rose: %.0f%% after %.0f%%", cover, lastCover)
		}
		lastCover = cover
	}
}

// An unmeasurable link must not guess a ratio off a bogus loss number; it falls
// back to the default gaming ratio every KCP preset already runs.
func TestRecommendFECUnusableFallsBack(t *testing.T) {
	p := RecommendFEC(PathQuality{Sent: 1, Received: 0})
	if p.Data != 10 || p.Parity != 3 {
		t.Fatalf("unusable link => %s, want the 10:3 default", p.Ratio())
	}
}

// The editable list parser skips comments and blanks and keeps well-formed rows.
func TestParseGameEndpoints(t *testing.T) {
	text := `# a comment
Dota 2|Europe West|185.25.183.1|Valve Lux

CS2|EU||missing host is dropped
Valorant|Europe|104.18.32.1
badline-no-pipes
`
	eps := parseGameEndpoints(text)
	if len(eps) != 2 {
		t.Fatalf("parsed %d endpoints, want 2: %+v", len(eps), eps)
	}
	if eps[0].Game != "Dota 2" || eps[0].Host != "185.25.183.1" || eps[0].Note != "Valve Lux" {
		t.Errorf("first row wrong: %+v", eps[0])
	}
	if eps[1].Game != "Valorant" || eps[1].Note != "" {
		t.Errorf("noteless row wrong: %+v", eps[1])
	}
}
