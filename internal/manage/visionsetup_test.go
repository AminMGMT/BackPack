package manage

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

func TestVisionAutomaticCoverScanWithInstalledXray(t *testing.T) {
	if os.Getenv("BP_VISION_SCAN_LIVE") == "" {
		t.Skip("opt-in external cover check")
	}
	binary := visionBinary(os.Getenv("BP_VISION_TEST_BINARY"))
	results := scanVisionCovers(context.Background(), visionCoverSuggestions, binary, probeVisionSetup)
	verified := 0
	for _, result := range results {
		t.Logf("%s: verified=%v elapsed=%s error=%v", result.target, result.err == nil, result.delay, result.err)
		if result.err == nil {
			verified++
		}
	}
	if verified == 0 {
		t.Fatal("no cover passed TLS and actual Vision authentication")
	}
}

func TestVisionScanOffersOnlyAuthenticatedCoversInSuggestionOrder(t *testing.T) {
	results := scanVisionCovers(context.Background(), []string{"blocked:443", "first:443", "fallback:443"}, "helper", func(ctx context.Context, target, sni, binary string) error {
		if sni+":443" != target || binary != "helper" {
			t.Error("probe lost SNI or binary")
		}
		if target == "blocked:443" {
			return errors.New("TLS rejected")
		}
		if target == "first:443" {
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	})
	if results[0].target != "first:443" || results[1].target != "fallback:443" || results[2].err == nil {
		t.Fatalf("bad recommendation: %+v", results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results = scanVisionCovers(ctx, []string{"cancelled:443"}, "helper", func(context.Context, string, string, string) error { return nil })
	if !errors.Is(results[0].err, context.Canceled) {
		t.Fatal("cancelled scan reported a verified cover")
	}
}

func TestVisionDefaultsGenerateIdentityAndBuildMatchingKharej(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	binary, _, _ := managedWizardFixture(t)
	s := TunnelSpec{Name: "vision-default-proof", Role: "server", Transport: "tcp", Token: "existing-token", Ports: []string{"8080=127.0.0.1:8081"}}
	ApplyPreset(&s, PresetBalance)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public := ln.Addr().String()
	ln.Close()
	restore := tui.SetInput(strings.NewReader(binary + "\n\n\n"))
	defer restore()
	var ok bool
	output := capture(t, func() {
		ok = setupManagedCarrierWithProbe(&s, "vision", public, "127.0.0.1", func(context.Context, string, string, string) error { return nil })
	})
	if !ok {
		t.Fatalf("defaults rejected: %s", output)
	}
	x := s.XrayServer
	if x.ServerName != "www.microsoft.com" || x.Target != "www.microsoft.com:443" || x.UUID == "" || x.ShortID == "" || x.PrivateKey == "" {
		t.Fatalf("incomplete automatic identity: %+v", x)
	}
	if strings.Contains(output, x.PrivateKey) || strings.Contains(output, s.Token) {
		t.Fatal("automatic setup printed a secret")
	}
	raw := pendingReverseLink(s, "203.0.113.1", linkExtras{})
	link, err := DecodeShareLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := kharejFromLink(link, LinkApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, pub, _ := managedVisionKey(x.PrivateKey)
	if peer.XrayClient.UUID != x.UUID || peer.XrayClient.ShortID != x.ShortID || peer.XrayClient.ServerName != x.ServerName || peer.XrayClient.PublicKey != pub || peer.Token != s.Token || peer.RemoteAddr != s.BindAddr {
		t.Fatal("link lost paired settings")
	}
	if !strings.Contains(output, pub) {
		t.Fatal("a manual Kharej setup has no way to learn the public key")
	}
	if peer.XrayServer.PrivateKey != "" || link.HelperPublicKey == x.PrivateKey {
		t.Fatal("private key escaped")
	}
}

func TestVisionManualCoverVerifiesTheChosenSNI(t *testing.T) {
	restore := tui.SetInput(strings.NewReader("invalid\nedge.example:8443\ncover.example\n"))
	defer restore()
	var target, sni string
	var ok bool
	capture(t, func() {
		target, sni, ok = manualVisionCover("", "", "helper", func(ctx context.Context, target, sni, binary string) error {
			if target != "edge.example:8443" || sni != "cover.example" {
				t.Fatal("manual SNI was not probed")
			}
			return nil
		})
	})
	if !ok || target != "edge.example:8443" || sni != "cover.example" {
		t.Fatal("custom endpoint lost")
	}
}

func TestVisionFailedScanCannotWriteUnverifiedCover(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	binary, _, _ := managedWizardFixture(t)
	s := TunnelSpec{Name: "vision-refusal", Role: "server", Transport: "tcp", Token: "keep-token"}
	before := s
	restore := tui.SetInput(strings.NewReader(binary + "\n0\n"))
	defer restore()
	var ok bool
	out := capture(t, func() {
		ok = setupManagedCarrierWithProbe(&s, "vision", "127.0.0.1:8443", "127.0.0.1", func(context.Context, string, string, string) error { return errors.New("blocked") })
	})
	if ok || !reflect.DeepEqual(s, before) || !strings.Contains(out, "No Suggested Site Passed") {
		t.Fatal("failed scan changed configuration")
	}
}

func TestVisionEditingKeepsCurrentPairedIdentityWithoutRescan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	binary, _, _ := managedWizardFixture(t)
	private, _, _ := managedVisionKey("")
	id, _ := managedUUID()
	s := TunnelSpec{Name: "vision-edit-proof", Role: "server", Transport: "tcp", BindAddr: "127.0.0.1:3080", Token: "keep-token"}
	s.XrayServer = config.XrayServerConfig{Binary: binary, Mode: config.XrayModeVision, Listen: "127.0.0.1:8443", UUID: id, ServerName: "existing.example", Target: "existing.example:443", ShortID: "0123456789abcdef", PrivateKey: private}
	before := s.XrayServer
	restore := tui.SetInput(strings.NewReader("\n\n\n\n"))
	defer restore()
	var ok bool
	capture(t, func() {
		ok = setupManagedCarrierWithProbe(&s, "vision", before.Listen, "127.0.0.1", func(context.Context, string, string, string) error {
			t.Error("keeping current settings must not rescan or replace the cover")
			return nil
		})
	})
	if !ok || s.XrayServer != before {
		t.Fatal("defaults replaced the saved identity")
	}
}
