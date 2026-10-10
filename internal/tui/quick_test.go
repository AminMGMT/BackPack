package tui

import (
	"strings"
	"testing"
)

func TestQuickDefaultsAllowSuggestionsAndOverrides(t *testing.T) {
	restore := SetInput(strings.NewReader(" \nchanged\n\n2\nn\n"))
	defer restore()
	defer QuickDefaults()()
	if PromptDefault("name", "suggested") != "suggested" {
		t.Fatal("space did not keep the default")
	}
	if PromptDefault("name", "suggested") != "changed" {
		t.Fatal("override ignored")
	}
	opts := []Option{{Title: "first"}, {Title: "second"}}
	if ChooseOptDefault("choice", opts, 1) != 1 || ChooseOptDefault("choice", opts, 0) != 1 {
		t.Fatal("suggested or explicit menu choice ignored")
	}
	if Confirm("create", true) {
		t.Fatal("explicit refusal ignored")
	}
}

func TestQuickScopeRestoresNormalMenuBehavior(t *testing.T) {
	restore := SetInput(strings.NewReader("\n"))
	defer restore()
	end := QuickDefaults()
	nested := QuickDefaults()
	nested()
	if !QuickDefaultsEnabled() {
		t.Fatal("nested scope lost quick defaults")
	}
	end()
	if QuickDefaultsEnabled() {
		t.Fatal("quick defaults leaked into ordinary menus")
	}
	if ChooseOpt("choice", []Option{{Title: "one"}}) != -1 {
		t.Fatal("ordinary menu accepted an empty choice")
	}
}
