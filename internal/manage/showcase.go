package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// The showcase: one square image on the panel's overview, with a link, whose
// content is published on a server the developer runs rather than in the
// binary — so it can change without a release.
//
// It is read the way the update check is: in the background, cached on disk,
// and never on the display path. The browser does not fetch it either. The
// panel's CSP admits images from its own origin only, and a panel in Iran
// cannot be assumed to reach the publishing server when its tunnel can; so the
// panel fetches it — direct, then through the tunnel relay, like an update —
// and serves the cached copy itself.
//
// What is published is a manifest and the file it names:
//
//	{"image": "ad.gif", "link": "https://t.me/channel", "title": "Text"}
//
// "image" is resolved against the manifest's own address, so a relative name
// keeps both in one directory. A manifest whose "image" is empty, or a
// manifest that is gone (404/410), clears the showcase; a network failure or a
// server error keeps the one already cached, because a blocked path is not the
// publisher saying anything.

// ShowcaseURL is where the manifest is published. Empty turns the whole thing
// off: nothing is fetched and the panel draws nothing. A variable so tests can
// point it at their own server.
var ShowcaseURL = app.ShowcaseURL

// ShowcaseDir holds the cached manifest and image.
var ShowcaseDir = app.ConfigDir + "/showcase"

const (
	// showcaseMaxManifest bounds the manifest. It is three short strings.
	showcaseMaxManifest = 16 << 10
	// showcaseMaxImage bounds the image. A long animated GIF runs to several
	// megabytes; anything past this is a mistake on the publishing side, and
	// every server running Backpack would download it.
	showcaseMaxImage = 15 << 20
	// showcaseMaxAge is how old the cached copy may be before a page view
	// starts a refresh.
	showcaseMaxAge = 30 * time.Minute
	// showcaseTimeout covers a whole request, body included. It is long
	// because the body may be the full image arriving through the tunnel
	// relay from Iran, and nothing waits on it: the refresh runs in the
	// background.
	showcaseTimeout = 2 * time.Minute
)

// Showcase is the cached state, as the panel reads it.
type Showcase struct {
	// Link is where a click goes; always http or https.
	Link string `json:"link,omitempty"`
	// Title is the image's alternative text and tooltip.
	Title string `json:"title,omitempty"`
	// Type is the sniffed media type of the cached image.
	Type string `json:"type,omitempty"`
	// Hash identifies the image bytes, so the browser can cache it by URL.
	Hash string `json:"hash,omitempty"`

	// Image is the absolute address the image was fetched from, and ETag and
	// Modified are its validators: an unchanged file costs a 304, and a file
	// replaced under the same name is still noticed.
	Image    string    `json:"image,omitempty"`
	ETag     string    `json:"etag,omitempty"`
	Modified string    `json:"modified,omitempty"`
	Checked  time.Time `json:"checked"`
}

// Present reports whether there is an image to draw.
func (s Showcase) Present() bool { return s.Hash != "" && s.Type != "" }

var (
	showcaseMu       sync.Mutex
	showcaseRunning  bool
	showcaseKickedAt time.Time
)

func showcaseStateFile() string { return filepath.Join(ShowcaseDir, "showcase.json") }
func showcaseImageFile() string { return filepath.Join(ShowcaseDir, "image") }

// LoadShowcase returns the cached showcase. It never touches the network.
func LoadShowcase() Showcase {
	var s Showcase
	_ = app.LoadState(showcaseStateFile(), &s)
	if s.Present() {
		if _, err := os.Stat(showcaseImageFile()); err != nil {
			return Showcase{}
		}
	}
	return s
}

// ShowcaseImage returns the cached image, its media type and its hash.
func ShowcaseImage() ([]byte, string, string, error) {
	s := LoadShowcase()
	if !s.Present() {
		return nil, "", "", os.ErrNotExist
	}
	b, err := os.ReadFile(showcaseImageFile())
	if err != nil {
		return nil, "", "", err
	}
	return b, s.Type, s.Hash, nil
}

// KickShowcase starts a background refresh when the cache is stale. It is
// called on every panel poll, so it gives up at once unless a refresh is both
// due and not already running, and asks at most once a minute whatever the
// outcome.
func KickShowcase() {
	if ShowcaseURL == "" {
		return
	}
	showcaseMu.Lock()
	defer showcaseMu.Unlock()
	if showcaseRunning || time.Since(showcaseKickedAt) < time.Minute {
		return
	}
	if time.Since(LoadShowcase().Checked) < showcaseMaxAge {
		return
	}
	showcaseRunning = true
	showcaseKickedAt = time.Now()
	go func() {
		_ = RefreshShowcase()
		showcaseMu.Lock()
		showcaseRunning = false
		showcaseMu.Unlock()
	}()
}

// RefreshShowcaseIfStale refreshes the cache when it is older than the panel
// lets it get, and waits for the answer. For the monitor service, which has no
// page view to start a refresh and runs its own loop to wait in.
func RefreshShowcaseIfStale() error {
	if ShowcaseURL == "" || time.Since(LoadShowcase().Checked) < showcaseMaxAge {
		return nil
	}
	return RefreshShowcase()
}

// errShowcaseGone is the publisher withdrawing the showcase.
var errShowcaseGone = errors.New("showcase withdrawn")

// RefreshShowcase fetches the manifest and, when it changed, the image.
func RefreshShowcase() error {
	if ShowcaseURL == "" {
		return nil
	}
	prev := LoadShowcase()

	var lastErr error
	for _, src := range sources(showcaseTimeout) {
		next, img, err := fetchShowcase(src.client, prev)
		if errors.Is(err, errShowcaseGone) {
			return clearShowcase()
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.name, err)
			continue
		}
		return saveShowcase(next, img)
	}
	return lastErr
}

// fetchShowcase reads the manifest and image through one client. img is nil
// when the cached image is still current.
func fetchShowcase(c *http.Client, prev Showcase) (Showcase, []byte, error) {
	body, status, _, err := showcaseGet(c, ShowcaseURL, nil, showcaseMaxManifest)
	if err != nil {
		return Showcase{}, nil, err
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return Showcase{}, nil, errShowcaseGone
	case status != http.StatusOK:
		return Showcase{}, nil, fmt.Errorf("manifest: HTTP %d", status)
	}

	var m struct {
		Image string `json:"image"`
		Link  string `json:"link"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return Showcase{}, nil, fmt.Errorf("manifest: %w", err)
	}
	if strings.TrimSpace(m.Image) == "" {
		return Showcase{}, nil, errShowcaseGone
	}
	imgURL, err := resolveShowcaseURL(ShowcaseURL, m.Image)
	if err != nil {
		return Showcase{}, nil, fmt.Errorf("manifest image: %w", err)
	}
	link := strings.TrimSpace(m.Link)
	if link != "" {
		if u, err := url.Parse(link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			// A link is optional; a bad one is dropped, not followed.
			link = ""
		}
	}

	next := Showcase{Link: link, Title: truncateRunes(strings.TrimSpace(m.Title), 120), Image: imgURL, Checked: time.Now()}

	// The validators are only worth sending for the same file.
	hdr := http.Header{}
	if prev.Present() && prev.Image == imgURL {
		if prev.ETag != "" {
			hdr.Set("If-None-Match", prev.ETag)
		}
		if prev.Modified != "" {
			hdr.Set("If-Modified-Since", prev.Modified)
		}
	}
	img, status, rh, err := showcaseGet(c, imgURL, hdr, showcaseMaxImage)
	if err != nil {
		return Showcase{}, nil, fmt.Errorf("image: %w", err)
	}
	switch {
	case status == http.StatusNotModified && prev.Present():
		next.Type, next.Hash, next.ETag, next.Modified = prev.Type, prev.Hash, prev.ETag, prev.Modified
		return next, nil, nil
	case status == http.StatusNotFound || status == http.StatusGone:
		return Showcase{}, nil, errShowcaseGone
	case status != http.StatusOK:
		return Showcase{}, nil, fmt.Errorf("image: HTTP %d", status)
	}
	// The type is read from the bytes, never from the header: it is served
	// back from the panel's own origin, and whatever is published there is
	// only ever drawn as an image.
	typ := http.DetectContentType(img)
	switch typ {
	case "image/gif", "image/png", "image/jpeg", "image/webp":
	default:
		return Showcase{}, nil, fmt.Errorf("image: %s is not a picture the panel shows", typ)
	}
	sum := sha256.Sum256(img)
	next.Type = typ
	next.Hash = hex.EncodeToString(sum[:8])
	next.ETag = rh.Get("ETag")
	next.Modified = rh.Get("Last-Modified")
	return next, img, nil
}

// showcaseGet reads at most max bytes of u, returning the body, the status
// and the response headers.
func showcaseGet(c *http.Client, u string, hdr http.Header, max int64) ([]byte, int, http.Header, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "Backpack/"+app.Version)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, 0, nil, err
	}
	if int64(len(b)) > max {
		return nil, 0, nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, resp.StatusCode, resp.Header, nil
}

// resolveShowcaseURL resolves the manifest's image reference against the
// manifest address, and accepts only http and https.
func resolveShowcaseURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	u := b.ResolveReference(r)
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%q is not an http(s) address", ref)
	}
	return u.String(), nil
}

// saveShowcase writes the image (when there is a new one) before the state
// that names it, so a reader never finds a state pointing at an older file
// with a newer hash.
func saveShowcase(s Showcase, img []byte) error {
	if img != nil {
		if err := app.WriteFileAtomic(showcaseImageFile(), img, 0644); err != nil {
			return err
		}
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return app.WriteFileAtomic(showcaseStateFile(), data, 0644)
}

// clearShowcase records that there is nothing to show, and when that was
// learnt, so the next refresh waits its turn rather than asking again.
func clearShowcase() error {
	_ = os.Remove(showcaseImageFile())
	data, _ := json.MarshalIndent(Showcase{Checked: time.Now()}, "", "  ")
	return app.WriteFileAtomic(showcaseStateFile(), data, 0644)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
