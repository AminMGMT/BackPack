package optimize

import "testing"

// ip_local_reserved_ports is what keeps a service's own port out of the range
// the kernel hands to outgoing connections. It is written as a comma-separated
// list with consecutive runs collapsed, which is how the kernel reads it back —
// and a forwarded range of a thousand ports has to come out as one range rather
// than a thousand entries.
func TestReservedListCollapsesRuns(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want string
	}{
		{"nothing", nil, ""},
		{"one", []int{62050}, "62050"},
		{"a pair", []int{62050, 62051}, "62050-62051"},
		{"unsorted", []int{62051, 443, 62050}, "443,62050-62051"},
		{"duplicates", []int{443, 443, 443}, "443"},
		{"a run and a stray", []int{10000, 10001, 10002, 20000}, "10000-10002,20000"},
		{"two runs", []int{1, 2, 3, 9, 10}, "1-3,9-10"},
		{"gaps of one are not a run", []int{1, 3, 5}, "1,3,5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reservedList(tt.in); got != tt.want {
				t.Errorf("reservedList(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A value that is not a port must not reach the sysctl: the kernel rejects the
// whole list, so one bad entry would lose every real reservation with it.
func TestReservedListDropsWhatIsNotAPort(t *testing.T) {
	got := reservedList([]int{0, -1, 65536, 99999, 62050})
	if got != "62050" {
		t.Errorf("reservedList kept something that is not a port: %q", got)
	}
}
