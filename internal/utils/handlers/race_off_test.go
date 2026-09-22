//go:build !race

package handlers

// raceEnabled is false in an ordinary run; see race_on_test.go.
const raceEnabled = false
