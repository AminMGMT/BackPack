package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// Reported against v1.8.4: "I turn the web panel off by hand, and every time I
// run the script with sudo backpack it turns itself back on." The menu started
// the panel on every run, and a stop left nothing behind to say it was meant.

// fakePanelUnit points the unit file at a temporary directory and records the
// starts instead of making them. It returns the number of starts so far.
func fakePanelUnit(t *testing.T) *int {
	t.Helper()
	unit := filepath.Join(t.TempDir(), "backpack-webui.service")
	prevPath, prevStart, prevRemove := panelUnitPath, installAndStart, removeUnit
	starts := 0
	panelUnitPath = func() string { return unit }
	installAndStart = func(body string) error {
		starts++
		return os.WriteFile(unit, []byte(body), 0o644)
	}
	removeUnit = func() error { return os.Remove(unit) }
	t.Cleanup(func() { panelUnitPath, installAndStart, removeUnit = prevPath, prevStart, prevRemove })
	return &starts
}

func TestAPanelTheOperatorStoppedStaysStoppedWhenTheMenuOpens(t *testing.T) {
	useConfigFile(t, Config{Password: "12345678", BasePath: "p"})
	starts := fakePanelUnit(t)
	if err := os.WriteFile(panelUnitPath(), []byte(panelUnit()), 0o644); err != nil {
		t.Fatal(err) // a panel that is installed and running
	}

	if _, started, err := StartUnlessStopped(); err != nil || !started {
		t.Fatalf("a panel nobody stopped was not started: started=%v err=%v", started, err)
	}
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // three more runs of `sudo backpack`
		if _, started, err := StartUnlessStopped(); err != nil || started {
			t.Fatalf("run %d started a panel the operator had stopped (err %v)", i+1, err)
		}
	}
	if *starts != 1 {
		t.Fatalf("the panel was started %d times; once, before the stop, is right", *starts)
	}

	// Restart panel is the operator changing their mind, and it sticks.
	if _, err := EnsureRunning(); err != nil {
		t.Fatal(err)
	}
	if _, started, _ := StartUnlessStopped(); !started {
		t.Fatal("after an explicit start the menu treated the panel as stopped")
	}
}

// A panel stopped under v1.8.4 left no flag: its unit was removed and its config
// kept. That is recognised, so upgrading does not switch it back on.
func TestAPanelStoppedByAnOlderVersionIsRecognised(t *testing.T) {
	useConfigFile(t, Config{Password: "12345678", BasePath: "p"})
	fakePanelUnit(t) // no unit file written: the state an old stop leaves

	if !StoppedByOperator() {
		t.Fatal("a configured panel with no unit was taken for a new install and would be started")
	}
}

// A new install has no config and no unit, and gets its panel.
func TestANewInstallGetsItsPanel(t *testing.T) {
	prev := configOverride
	configOverride = filepath.Join(t.TempDir(), "webui.json")
	t.Cleanup(func() { configOverride = prev })
	starts := fakePanelUnit(t)

	if _, started, err := StartUnlessStopped(); err != nil || !started || *starts != 1 {
		t.Fatalf("a new install's panel was not started: started=%v starts=%d err=%v", started, *starts, err)
	}
}
