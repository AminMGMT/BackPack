package manage

import (
	"reflect"
	"testing"
)

func TestAddForwardedPortKeepsExistingEntries(t *testing.T) {
	original := []string{"443", "8080=127.0.0.1:8080"}
	got, err := addForwardedPort(original, " 8443 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"443", "8080=127.0.0.1:8080", "8443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	if original[0] != "443" || len(original) != 2 {
		t.Fatalf("input was modified: %v", original)
	}
}

func TestReplaceForwardedPortChangesOnlySelectedEntry(t *testing.T) {
	original := []string{"443", "8080", "9000-9010"}
	got, err := replaceForwardedPort(original, 1, "8081=10.0.0.2:80")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"443", "8081=10.0.0.2:80", "9000-9010"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	if original[1] != "8080" {
		t.Fatalf("input was modified: %v", original)
	}
}

func TestRemoveForwardedPortKeepsRemainingEntries(t *testing.T) {
	got, err := removeForwardedPort([]string{"443", "8080", "8443"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"443", "8443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
}

func TestForwardedPortEditsRejectInvalidInput(t *testing.T) {
	if _, err := addForwardedPort([]string{"443"}, "not-a-port"); err == nil {
		t.Fatal("adding an invalid port succeeded")
	}
	if _, err := replaceForwardedPort([]string{"443"}, 2, "8080"); err == nil {
		t.Fatal("replacing an out-of-range port succeeded")
	}
	if _, err := removeForwardedPort([]string{"443"}, 0); err == nil {
		t.Fatal("removing the last port succeeded")
	}
	if _, err := replaceForwardedPorts(" , "); err == nil {
		t.Fatal("replacing the list with an empty value succeeded")
	}
}

func TestForwardedPortEditsRejectDuplicateListeners(t *testing.T) {
	if _, err := addForwardedPort([]string{"443=127.0.0.1:443"}, "443=127.0.0.1:8443"); err == nil {
		t.Fatal("adding the same exposed port with a different backend succeeded")
	}
	if _, err := replaceForwardedPort([]string{"443", "8080"}, 1, "443"); err == nil {
		t.Fatal("editing an entry onto an existing exposed port succeeded")
	}
}

func TestForwardedPortValidationFindsOverlappingListeners(t *testing.T) {
	for _, ports := range [][]string{
		{"443", "443"},
		{"400-450", "425"},
		{"443", "127.0.0.1:443=127.0.0.1:8443"},
		{"[::]:443=127.0.0.1:443", "192.0.2.1:443=127.0.0.1:8443"},
	} {
		if err := validatePortSpecs(ports); err == nil {
			t.Errorf("overlapping listeners were accepted: %v", ports)
		}
	}

	if err := validatePortSpecs([]string{
		"127.0.0.1:443=127.0.0.1:8443",
		"192.0.2.1:443=127.0.0.1:9443",
	}); err != nil {
		t.Fatalf("distinct listen addresses were rejected: %v", err)
	}
}
