package telegram

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

func TestShowcaseIsDueOnFridayFromNoonInTehran(t *testing.T) {
	// 2026-11-13 is a Friday. Tehran is UTC+03:30.
	for _, tc := range []struct {
		utc  string
		want string
	}{
		{"2026-11-12T20:00:00Z", ""},           // Thursday 23:30 in Tehran
		{"2026-11-13T08:29:00Z", ""},           // Friday 11:59
		{"2026-11-13T08:30:00Z", "2026-11-13"}, // Friday 12:00
		{"2026-11-13T20:29:00Z", "2026-11-13"}, // Friday 23:59
		{"2026-11-13T20:30:00Z", ""},           // Saturday 00:00
	} {
		now, _ := time.Parse(time.RFC3339, tc.utc)
		if got := showcaseFriday(now); got != tc.want {
			t.Errorf("%s: due for %q, want %q", tc.utc, got, tc.want)
		}
	}
}

// fakeBotAPI answers sendAnimation as Telegram does and keeps what it was
// sent. Chats listed in fail are refused.
type fakeBotAPI struct {
	mu    sync.Mutex
	calls []map[string]string // form fields; "animation" holds "<bytes N>" for an upload
	fail  map[string]bool
}

func (f *fakeBotAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	mr := multipart.NewReader(r.Body, params["boundary"])
	fields := map[string]string{"method": r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		if p.FileName() != "" {
			fields[p.FormName()] = "<upload " + p.FileName() + ">"
		} else {
			fields[p.FormName()] = string(b)
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, fields)
	f.mu.Unlock()
	if f.fail[fields["chat_id"]] {
		return &http.Response{StatusCode: 403, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"Forbidden: bot was blocked by the user"}`))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"animation":{"file_id":"FILE-1"}}}`))}, nil
}

func publishTestShowcase(t *testing.T) {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{color.White})
	var g bytes.Buffer
	gif.Encode(&g, img, nil)
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".json") {
			w.Write([]byte(`{"image":"ad.gif","link":"https://example.com/x","title":"Example & Co"}`))
			return
		}
		w.Write(g.Bytes())
	}))
	defer pub.Close()
	oldURL, oldDir := manage.ShowcaseURL, manage.ShowcaseDir
	manage.ShowcaseURL, manage.ShowcaseDir = pub.URL+"/ad.json", t.TempDir()
	t.Cleanup(func() { manage.ShowcaseURL, manage.ShowcaseDir = oldURL, oldDir })
	if err := manage.RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
}

func TestShowcaseReachesEveryAdminOnceAFriday(t *testing.T) {
	publishTestShowcase(t)
	old := showcaseSentFile
	showcaseSentFile = filepath.Join(t.TempDir(), "sent.json")
	t.Cleanup(func() { showcaseSentFile = old })

	api := &fakeBotAPI{fail: map[string]bool{"300": true}}
	oldT := http.DefaultTransport
	http.DefaultTransport = api
	t.Cleanup(func() { http.DefaultTransport = oldT })

	c := Config{Token: "1:tok", AdminID: "100", Admins: []Admin{{ID: "200"}, {ID: "300"}}}
	if err := sendShowcase(c, "2026-11-13"); err == nil {
		t.Error("a refused delivery was not reported")
	}
	if len(api.calls) != 3 {
		t.Fatalf("%d sends, want one per admin: %v", len(api.calls), api.calls)
	}
	first, second := api.calls[0], api.calls[1]
	if first["method"] != "sendAnimation" || first["animation"] != "<upload showcase.gif>" {
		t.Errorf("first send = %v, want the GIF uploaded as an animation", first)
	}
	if second["animation"] != "FILE-1" {
		t.Errorf("second send = %v, want Telegram's file id rather than a second upload", second)
	}
	if !strings.Contains(first["reply_markup"], `"url":"https://example.com/x"`) {
		t.Errorf("keyboard = %s, want the link as a button", first["reply_markup"])
	}
	if first["caption"] != "<b>Example &amp; Co</b>" {
		t.Errorf("caption = %q, want the title escaped for HTML", first["caption"])
	}

	// Later the same Friday: only the admin it failed for is tried again.
	api.calls, api.fail = nil, nil
	if err := sendShowcase(c, "2026-11-13"); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 1 || api.calls[0]["chat_id"] != "300" {
		t.Errorf("retry sent %v, want only chat 300", api.calls)
	}
	api.calls = nil
	if err := sendShowcase(c, "2026-11-13"); err != nil || len(api.calls) != 0 {
		t.Errorf("a third pass sent %d messages (err %v), want none", len(api.calls), err)
	}

	// The next Friday is a new week for everyone.
	if err := sendShowcase(c, "2026-11-20"); err != nil || len(api.calls) != 3 {
		t.Errorf("next Friday sent %d messages (err %v), want 3", len(api.calls), err)
	}
}

func TestShowcaseWithNothingPublishedSendsNothing(t *testing.T) {
	oldURL, oldDir := manage.ShowcaseURL, manage.ShowcaseDir
	manage.ShowcaseURL, manage.ShowcaseDir = "", t.TempDir()
	t.Cleanup(func() { manage.ShowcaseURL, manage.ShowcaseDir = oldURL, oldDir })
	old := showcaseSentFile
	showcaseSentFile = filepath.Join(t.TempDir(), "sent.json")
	t.Cleanup(func() { showcaseSentFile = old })

	api := &fakeBotAPI{}
	oldT := http.DefaultTransport
	http.DefaultTransport = api
	t.Cleanup(func() { http.DefaultTransport = oldT })

	if err := sendShowcase(Config{Token: "1:tok", AdminID: "100"}, "2026-11-13"); err != nil || len(api.calls) != 0 {
		t.Errorf("sent %d messages (err %v) with nothing published", len(api.calls), err)
	}
}
