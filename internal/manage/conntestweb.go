package manage

import "time"

// What the web panel's Connection Test section needs from the test that the
// menu runs. The panel drives the same StartConnTestIran → Joined → Run →
// Fetched sequence connTestIranMenu does; these are the few facts it would
// otherwise have to copy.

// ConnTestJoinWait is how long the Iran side waits for the kharej to check in.
func ConnTestJoinWait() time.Duration { return connTestJoinWait }

// ConnTestRunTime is about how long the tunnels take to be judged once the
// kharej has joined: they are tried side by side, each given its connect wait
// and its one-second echoes, and then the speed runs and the path MTU arrives.
// It is what the panel counts down from, so it errs a little long rather than
// reaching zero with rows still filling.
func ConnTestRunTime() time.Duration {
	return ctStabilityTime() + ConnTestSpeedTime(ctSpeedTunnels())
}

// connTestRunLimit is the longest the tunnels can take to be judged: every
// one of them using the whole of its speed budget in both passes. It is what
// the kharej keeps its tunnels up for, where ConnTestRunTime is what a test
// usually takes and the panel counts down from.
//
// The kharej used to be held to ConnTestRunTime plus connTestSlack. Once the
// speed test climbed to sixty-four connections, a fast path took longer than
// that estimate, the kharej stopped its tunnels while the Iran side was still
// measuring, and the rows measured last — the direct tunnels, after every
// reverse one — failed their speed test.
func connTestRunLimit() time.Duration {
	return ctStabilityTime() + time.Duration(ctSpeedTunnels())*ctSpeedTunnelLimit()
}

// ctStabilityTime is the stability phase: the connect wait, the echoes, and
// time to spare for the rows to settle.
func ctStabilityTime() time.Duration {
	return connTestConnectWait + time.Duration(connTestSoak)*time.Second + 45*time.Second
}

// ctSpeedTunnels is how many tunnels the speed phase is counted for.
func ctSpeedTunnels() int { return len(connTestReverse) - 1 + len(connTestDirect) }

// ctSpeedTunnelLimit is the most one tunnel's two speed passes take: the first
// is cut off at ctSpeedBudget, and the second measures each direction once,
// with time for its connections to open.
func ctSpeedTunnelLimit() time.Duration {
	return ctSpeedBudget + ctSpeedSecondPass() + 10*time.Second
}

// ConnTestSpeedTime estimates the serial speed phase for healthy tunnels:
// both directions at every connection count, then both once more in the
// second pass.
func ConnTestSpeedTime(tunnels int) time.Duration {
	return time.Duration(tunnels) * (ctSpeedFirstPass() + ctSpeedSecondPass())
}

// ConnTestSpeedLeft estimates what is left of the speed phase from the rows'
// phases, or 0 when it has not started.
func ConnTestSpeedLeft(phases []string) time.Duration {
	var left time.Duration
	started := false
	for _, p := range phases {
		switch p {
		case "speed-queued":
			left += ctSpeedFirstPass() + ctSpeedSecondPass()
		case "speed":
			started = true
			left += ctSpeedFirstPass() + ctSpeedSecondPass()
		case ctPhaseAgain, ctPhaseSecond:
			started = true
			left += ctSpeedSecondPass()
		}
	}
	if !started {
		return 0
	}
	return left
}

// ConnTestSpeedPhase is a phase in which a row is being measured, in either
// pass.
func ConnTestSpeedPhase(phase string) bool { return phase == "speed" || phase == ctPhaseSecond }

// ctSpeedFirstPass and ctSpeedSecondPass are what one healthy tunnel's two
// speed passes take.
//
// The first pass is counted at one stage past the ones that always run: a
// healthy tunnel climbs past sixteen connections on a fast path and stops
// there or at the next. Counting all of ctSpeedStages would put most tests
// minutes behind their countdown.
func ctSpeedFirstPass() time.Duration {
	return time.Duration(2*(ctSpeedAlways+1))*(ctSpeedWarmup+ctSpeedMaximum) + time.Second
}

func ctSpeedSecondPass() time.Duration { return 2*(ctSpeedWarmup+ctSpeedMaximum) + time.Second }

// ConnTestName is a tested transport's name as the menus print it: "TCP MUX",
// "KCP FEC", "Iran→Kharej".
func ConnTestName(tr string) string { return ctName(tr) }

// ConnTestNameOf is ConnTestName for a row of the given kind: "xDiRvs" for
// the reverse xdi, "xDi" for the direct one.
func ConnTestNameOf(kind, tr string) string { return ctNameOf(kind, tr) }
