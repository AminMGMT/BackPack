package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConnectionTestPanelAdvertisesAddressCandidatesWithoutStartingATest(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	s.handleConnTest(w, httptest.NewRequest(http.MethodGet, "/api/conntest", nil))
	var v conntestView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.State != ctIdle || v.Addresses == nil {
		t.Fatalf("bad discovery response: %d %s", w.Code, w.Body)
	}
	if s.conntest.busy() {
		t.Fatal("reading address candidates started a test")
	}
}

func TestConnectionTestPanelRejectsWrongFamiliesBeforePreparingTunnels(t *testing.T) {
	for _, fields := range []url.Values{
		{"action": {"start"}, "path": {"both"}, "ipv4": {"127.0.0.1"}},
		{"action": {"start"}, "path": {"ipv6"}, "ipv6": {"127.0.0.1"}},
		{"action": {"start"}, "path": {"ipv4"}, "ipv4": {"::1"}},
		{"action": {"start"}, "path": {"wrong"}},
	} {
		s := &server{}
		r := httptest.NewRequest(http.MethodPost, "/api/conntest", strings.NewReader(fields.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleConnTest(w, r)
		if w.Code != http.StatusBadRequest || s.conntest.busy() {
			t.Fatalf("invalid request %v: %d %s", fields, w.Code, w.Body)
		}
	}
}

func TestConnectionTestSnapshotsOwnTheirPerPathRows(t *testing.T) {
	c := &conntestRunner{v: conntestView{Paths: []conntestPath{{Host: "::1", Rows: []conntestRow{{Status: "testing"}}}}}}
	v := c.view()
	v.Paths[0].Rows[0].Status = "changed"
	v.Paths[0].Host = "changed"
	if got := c.view().Paths[0]; got.Host != "::1" || got.Rows[0].Status != "testing" {
		t.Fatalf("snapshot modified a live path: %+v", got)
	}
}
