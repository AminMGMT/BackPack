package manage

import "time"

// What the web panel's Connection Test section needs from the test that the
// menu runs. The panel drives the same StartConnTestIran → Joined → Run →
// Fetched sequence connTestIranMenu does; these are the few facts it would
// otherwise have to copy.

// ConnTestHost is this server's address as a kharej would dial it — the
// default the menu offers — or empty when it is not known.
func ConnTestHost() string { return linkHost() }

// ConnTestJoinWait is how long the Iran side waits for the kharej to check in.
func ConnTestJoinWait() time.Duration { return connTestJoinWait }

// ConnTestRunTime is about how long the tunnels take to be judged once the
// kharej has joined: they are tried side by side, each given its connect wait
// and its one-second echoes, and then the speed runs and the path MTU arrives.
// It is what the panel counts down from, so it errs a little long rather than
// reaching zero with rows still filling.
func ConnTestRunTime() time.Duration {
	return connTestConnectWait + time.Duration(connTestSoak)*time.Second + 45*time.Second
}

// ConnTestName is a tested transport's name as the menus print it: "TCP MUX",
// "KCP FEC", "Iran→Kharej".
func ConnTestName(tr string) string { return ctName(tr) }
