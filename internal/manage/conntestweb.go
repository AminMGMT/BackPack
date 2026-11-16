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
	return connTestConnectWait + time.Duration(connTestSoak)*time.Second + 45*time.Second +
		ConnTestSpeedTime(len(connTestReverse)-1+len(connTestDirect))
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

func ctSpeedFirstPass() time.Duration {
	return time.Duration(2*len(ctSpeedStages))*(ctSpeedWarmup+ctSpeedMaximum) + time.Second
}

func ctSpeedSecondPass() time.Duration { return 2*(ctSpeedWarmup+ctSpeedMaximum) + time.Second }

// ConnTestName is a tested transport's name as the menus print it: "TCP MUX",
// "KCP FEC", "Iran→Kharej".
func ConnTestName(tr string) string { return ctName(tr) }
