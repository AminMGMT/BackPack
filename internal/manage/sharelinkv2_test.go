package manage

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Every field a link carries has a number in format 2, or a link would lose it
// on the way.
func TestEveryShareLinkFieldHasAnID(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range shareFieldIDs {
		if ids[id] {
			t.Errorf("%q is numbered twice", id)
		}
		ids[id] = true
	}
	typ := reflect.TypeOf(ShareLink{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "v" && !ids[name] {
			t.Errorf("ShareLink.%s (%q) has no number in shareFieldIDs; add it at the end", typ.Field(i).Name, name)
		}
	}
}

// The same link at half the length: a reverse tunnel the wizard printed at
// 264 characters in format 1.
func TestFormat2IsShortAndCarriesTheSameLink(t *testing.T) {
	iran := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: "0.0.0.0:443",
		Ports: []string{"3030", "8080"}, Token: randomToken(64)}
	ApplyPreset(&iran, PresetTurbo)
	raw := pendingReverseLink(iran, "94.139.180.179", linkExtras{})
	if !strings.HasPrefix(raw, "backpack://2.") || len(raw) > 140 {
		t.Errorf("the link is %d characters: %s", len(raw), raw)
	}
	got, err := DecodeShareLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tok != iran.Token || got.Name != "server-443" || got.Host != "94.139.180.179" ||
		got.Port != "443" || got.Tr != "tcp" || got.Ports != "3030, 8080" || got.Preset != PresetTurbo {
		t.Errorf("the link came back different: %+v", got)
	}

	// A name that is not the default, a token that is not the token alphabet,
	// a host that is a name, a false *bool and a list all survive.
	off := false
	odd := ShareLink{Kind: "direct", From: "iran", Name: "my-tunnel", Tok: "not/base62+token==",
		Tr: "pck", Port: "9000", Host: "iran.example.com", AutoMTU: &off,
		Hosts: []string{"2.3.4.5", "b.example.com:443"}, GREKey: 4000000000, MSS: -1}
	s, err := odd.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeShareLink(s)
	if err != nil {
		t.Fatal(err)
	}
	odd.V = 1
	if !reflect.DeepEqual(back, odd) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, odd)
	}
	// An empty name stays empty rather than becoming the default.
	odd.Name = ""
	s, _ = odd.Encode()
	if back, _ = DecodeShareLink(s); back.Name != "" {
		t.Errorf("an empty name came back as %q", back.Name)
	}
}

// Links made by older builds are format 1, and keep working.
func TestAFormat1LinkStillDecodes(t *testing.T) {
	want := ShareLink{V: 1, Kind: "reverse", From: "iran", Name: "server-443", Tok: randomToken(64),
		Tr: "wss", Port: "443", Host: "1.2.3.4", Preset: "turbo", Ports: "3030"}
	raw, _ := json.Marshal(want)
	v1 := shareScheme + shareVersion + "." + gzipB64(raw)
	got, err := DecodeShareLink(v1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("format 1:\n got %+v\nwant %+v", got, want)
	}
}

// A changed character is refused, not read as another link.
func TestAFormat2LinkWithAChangedCharacterIsRefused(t *testing.T) {
	s, _ := sampleLink().Encode()
	body := []byte(strings.TrimPrefix(s, "backpack://2."))
	raw, _ := base64.RawURLEncoding.DecodeString(string(body))
	raw[len(raw)/2] ^= 0x01
	if _, err := DecodeShareLink("backpack://2." + base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Error("a link with a flipped bit was accepted")
	}
}
