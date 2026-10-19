package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

func TestHelperReadinessStartsPendingForEachManagedGeneration(t *testing.T) {
	e := &engineControl{name: "fixture"}
	for _, cfg := range []*config.Config{
		{Server: config.ServerConfig{BindAddr: "127.0.0.1:62001", Naive: config.NaiveServerConfig{Binary: "/helper"}}},
		{Client: config.ClientConfig{RemoteAddr: "127.0.0.1:62001", Xray: config.XrayClientConfig{Binary: "/helper"}}},
	} {
		e.setGeneration(func() {}, cfg)
		if s := e.Status(); s.HelperReady == nil || *s.HelperReady {
			t.Fatalf("managed generation did not start pending: %+v", s)
		}
		e.mu.Lock()
		*e.helperReady = true
		e.mu.Unlock()
		prior := e.Status()
		e.setGeneration(func() {}, cfg)
		if s := e.Status(); s.HelperReady == nil || *s.HelperReady || !*prior.HelperReady {
			t.Fatal("new generation inherited readiness or changed an earlier snapshot")
		}
	}
	e.setGeneration(func() {}, &config.Config{Client: config.ClientConfig{RemoteAddr: "127.0.0.1:62001"}})
	status := e.Status()
	body, err := json.Marshal(status)
	if err != nil || status.HelperReady != nil || strings.Contains(string(body), "helperReady") {
		t.Fatal("ordinary transports acquired a managed helper readiness requirement")
	}
}
