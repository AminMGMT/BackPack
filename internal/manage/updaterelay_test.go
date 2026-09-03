package manage

import (
	"strings"
	"testing"
)

// The cheapest way out is offered first.
//
// A tunnel that already exposes the relay port can be used as it stands; one
// that does not has to be restarted to gain it, which interrupts whatever it is
// carrying. An operator picking from a list should not have to work out which
// is which.
func TestTheTunnelThatCostsNothingIsOfferedFirst(t *testing.T) {
	got := orderRelayOptions([]RelayOption{
		{Name: "zed", Ready: false},
		{Name: "beta", Ready: true},
		{Name: "alpha", Ready: false},
		{Name: "acme", Ready: true},
	})
	var names []string
	for _, o := range got {
		names = append(names, o.Name)
	}
	if strings.Join(names, ",") != "acme,beta,alpha,zed" {
		t.Errorf("offered in the order %v", names)
	}
}

// A choice made for one run is remembered for the whole of it.
//
// The tag, the raw version file, the asset and its checksums are four fetches
// that make up one update. Reading the version through a tunnel and then trying
// to fetch the binary directly would fail halfway, with an error that describes
// neither half.
func TestAChosenTunnelIsRememberedAndCanBeCleared(t *testing.T) {
	t.Cleanup(func() { UseRelay("") })

	if RelayChosen() {
		t.Fatal("a tunnel was chosen before anything asked for one")
	}
	UseRelay("fr-relay")
	if !RelayChosen() {
		t.Error("the choice was not remembered")
	}
	UseRelay("")
	if RelayChosen() {
		t.Error("clearing the choice left one behind")
	}
}

// And with no choice made, the ordinary order stands: direct first, because
// that is the one that costs nothing when it works.
func TestWithNoChoiceTheDirectRouteIsTriedFirst(t *testing.T) {
	t.Cleanup(func() { UseRelay("") })
	UseRelay("")

	got := sources(0)
	if len(got) == 0 || got[0].name != "direct" {
		t.Fatalf("the first source is %+v, want direct", got)
	}
}
