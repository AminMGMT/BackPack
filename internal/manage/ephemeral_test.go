package manage

import "testing"

// A machine whose ephemeral range reaches past the kernel's default can lose a
// service's port to an outgoing connection. Health Check is the only thing that
// will ever tell an existing install, because updating does not rewrite the
// sysctl file that put it there.
func TestEphemeralRangeIsWide(t *testing.T) {
	tests := []struct {
		name string
		in   string
		lo   int
		hi   int
		wide bool
	}{
		{"the kernel's own", "32768 60999", 32768, 60999, false},
		{"tab separated, as /proc writes it", "32768\t60999", 32768, 60999, false},

		// What older versions of Optimize wrote, and what was found on the
		// server this check comes from.
		{"widened both ends", "1024 65535", 1024, 65535, true},

		// Each end on its own. The ceiling is the one that matters for a panel
		// node on 62050: the floor could be correct and the port still exposed.
		{"floor lowered", "1024 60999", 1024, 60999, true},
		{"ceiling raised", "32768 65535", 32768, 65535, true},

		// Narrower than the default is a deliberate choice, not a hazard.
		{"narrowed", "40000 50000", 40000, 50000, false},

		{"empty", "", 0, 0, false},
		{"one field", "32768", 0, 0, false},
		{"not numbers", "low high", 0, 0, false},
		{"backwards", "60999 32768", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi, wide := ephemeralRangeIsWide(tt.in)
			if lo != tt.lo || hi != tt.hi || wide != tt.wide {
				t.Errorf("ephemeralRangeIsWide(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tt.in, lo, hi, wide, tt.lo, tt.hi, tt.wide)
			}
		})
	}
}

// 62050 and 62051 are the ports this whole check exists for: they sit above the
// default range and inside the widened one.
func TestThePanelNodePortsAreOutsideTheDefaultRange(t *testing.T) {
	for _, p := range []int{62050, 62051} {
		if p >= ephemeralDefaultLow && p <= ephemeralDefaultHigh {
			t.Errorf("%d is inside the default ephemeral range, so restoring the range "+
				"would not protect it and the reservation is doing all the work", p)
		}
		if _, _, wide := ephemeralRangeIsWide("1024 65535"); !wide {
			t.Fatal("the range that exposed these ports is not reported as wide")
		}
	}
}
