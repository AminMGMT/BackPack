package schedule

import "testing"

// "There is no crontab" and "the crontab could not be read" are not the same
// answer, and treating them as one is how installing a job deletes every other
// job on the machine.
//
// readCrontab returned nil for every failure of `crontab -l`, and SetCron built
// the new crontab from that nil — which is a crontab containing exactly one
// line, ours. So a transient failure turned "schedule an auto-refresh" into
// "replace this machine's crontab with a single backpack entry": the operator's
// backups, their certificate renewals, their own scripts, gone, and the program
// reporting success.
//
// The distinction is made on what cron says. Every implementation in use
// answers a user with no crontab with those words.
func TestAnEmptyCrontabIsToldApartFromOneThatCouldNotBeRead(t *testing.T) {
	empty := []string{
		"no crontab for root\n",
		"no crontab for amin",
		"crontab: no crontab for root",
		"No crontab for root",
	}
	for _, msg := range empty {
		if !noCrontabYet([]byte(msg)) {
			t.Errorf("%q was read as a failure, so scheduling anything on a machine with "+
				"no crontab yet would refuse", msg)
		}
	}

	failures := []string{
		"crontab: installing new crontab\ncrontab: error renaming",
		"/var/spool/cron/crontabs/root: Permission denied",
		"crontab: cannot read /var/spool/cron: Input/output error",
		"",
		"seteuid: Operation not permitted",
	}
	for _, msg := range failures {
		if noCrontabYet([]byte(msg)) {
			t.Errorf("%q was read as an empty crontab — a write built on that replaces "+
				"every job on the machine with ours", msg)
		}
	}
}
