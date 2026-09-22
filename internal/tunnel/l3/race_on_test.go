//go:build race

package l3

// raceEnabled is true when the race detector is on.
//
// The benchmark gates next door measure wall-clock time, and the race detector
// multiplies it by somewhere between five and twenty. That is not a regression
// and there is no ceiling that is meaningful in both modes, so the gates skip
// here rather than being loosened until they catch nothing. The allocation
// half of each gate is unaffected and still runs.
const raceEnabled = true
