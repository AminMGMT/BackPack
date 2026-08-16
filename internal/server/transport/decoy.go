package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The identity the decoy site wears.
//
// Answering a probe with a plausible page is only half of the job. The other
// half is that it must not be the *same* plausible page on every install, and
// until now it was: every Backpack server on earth returned byte-identical
// bytes — one trimmed welcome page, `Server: nginx`, and nothing else. No
// Last-Modified, no ETag, no Accept-Ranges, the same Content-Length everywhere.
//
// That inverts what the decoy is for. A single server looked unremarkable, but
// the fleet became enumerable: one internet-wide scan for that exact response
// finds every Backpack server there is, no token and no probing required. A
// camouflage shared by everyone wearing it is a uniform.
//
// So the identity is derived from the tunnel token — which nginx build the
// server claims to be, whether that build was compiled to print its version,
// when its index.html was last written, and therefore its ETag. The token is
// secret and different on every install, so the values cannot be predicted from
// outside and no two servers share them. It is a hash rather than a random
// draw, so one server keeps its identity across restarts — a real file on a
// real disk does not change its date when the machine reboots.
//
// The other half of looking real is answering like a static file server rather
// than like a program with one canned reply. Stock nginx serves index.html at
// `/` and a 404 everywhere else; it honours conditional and range requests. It
// does not return the same 200 HTML for every path anybody asks for.
type decoyProfile struct {
	// server is the Server header, and also the footer nginx prints on its own
	// error pages — in nginx these are the same string, so they are one field
	// here and can never drift apart.
	server   string
	page     string
	notFound string // the 404 body template; %s is the footer
	modTime  time.Time
}

// nginxBuild is one plausible nginx identity. The pages are not decoration:
// nginx changed both its default index and its error-page markup in the 1.23
// series, so a version string from 2020 shipping the 2023 page is exactly the
// kind of internal contradiction a fingerprinter looks for. Each build carries
// the pages that build actually ships.
type nginxBuild struct {
	version  string // what follows "nginx/" when server tokens are on
	page     string // the default index.html this build installs
	notFound string // this build's 404 body; %s is the footer
}

// Real distro package versions, so a chosen value is one that genuinely exists
// in the wild rather than something synthesised.
var nginxBuilds = []nginxBuild{
	{"1.14.0 (Ubuntu)", indexClassic, notFoundClassic}, // 18.04
	{"1.18.0 (Ubuntu)", indexClassic, notFoundClassic}, // 20.04
	{"1.20.1", indexClassic, notFoundClassic},          // RHEL 9 / Alma
	{"1.22.1", indexClassic, notFoundClassic},          // Debian 12
	{"1.24.0 (Ubuntu)", indexModern, notFoundModern},   // 24.04
	{"1.26.3", indexModern, notFoundModern},            // recent stable
}

// modTimeAnchor is the newest date the decoy's index.html may claim. Every
// profile lands somewhere in the window below it, so the file is always in the
// past — a Last-Modified ahead of the response's own Date is one of the few
// things a static file genuinely cannot do. An index.html a year or three old
// is the ordinary case: it is written once at install and never touched again,
// so this ages gracefully and needs no upkeep.
var modTimeAnchor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	modTimeMinAge = 30  // days
	modTimeMaxAge = 900 // days
)

// newDecoyProfile derives a stable, per-install identity from the tunnel token.
func newDecoyProfile(token string) decoyProfile {
	// Domain-separated from every other thing the token keys, so that publishing
	// a decoy's ETag can never leak anything about the credential itself.
	h := sha256.Sum256([]byte("backpack-decoy\x00" + token))

	build := nginxBuilds[int(h[0])%len(nginxBuilds)]

	// `server_tokens off` is common enough on hardened hosts to be unremarkable,
	// and it hides the version from the header and the error page alike. Half the
	// installs take it, which splits the fleet across one more axis.
	server := "nginx"
	if h[1]&1 == 0 {
		server = "nginx/" + build.version
	}

	// A clock set behind the anchor would otherwise date the file in the future.
	anchor := modTimeAnchor
	if now := time.Now().UTC(); now.Before(anchor) {
		anchor = now
	}
	days := int(binary.BigEndian.Uint16(h[2:4]))%(modTimeMaxAge-modTimeMinAge) + modTimeMinAge
	secs := int(binary.BigEndian.Uint32(h[4:8]) % 86400)
	modTime := anchor.Add(-time.Duration(days)*24*time.Hour - time.Duration(secs)*time.Second)

	return decoyProfile{
		server:   server,
		page:     build.page,
		notFound: build.notFound,
		modTime:  modTime,
	}
}

// etag reproduces nginx's format for a static file: the modification time and
// the size, both hex, joined by a dash.
//
// It is computed rather than drawn so it cannot contradict the two headers it
// is made of. An ETag that is random next to its own Last-Modified and
// Content-Length is worse than none at all — nothing serving files produces
// that combination, so it identifies the server as something pretending.
func (p decoyProfile) etag() string {
	return fmt.Sprintf(`"%x-%x"`, p.modTime.Unix(), len(p.page))
}

// serve answers one non-tunnel request the way the nginx this profile claims to
// be would answer it.
func (p decoyProfile) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		p.serveNotFound(w, r)
		return
	}

	w.Header().Set("Server", p.server)
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("ETag", p.etag())

	// ServeContent is doing the file server's job here, not saving a few lines:
	// Last-Modified, Accept-Ranges, Content-Length, If-Modified-Since,
	// If-None-Match, Range and HEAD all have to behave the way they do for a
	// file on disk. Hand-writing a 200 gets every one of those wrong, and a
	// probe that sends the ETag back and is answered 200 instead of 304 has
	// learned the page is generated.
	http.ServeContent(w, r, "", p.modTime, strings.NewReader(p.page))
}

// serveNotFound is what stock nginx returns for a path that is not a file in
// its root — which is every path except the index, the tunnel's own included.
//
// Serving the welcome page on every path was the older behaviour and it was a
// tell twice over: no static site answers 200 for arbitrary paths, and the
// tunnel path answering exactly like every other path was only reassuring while
// every other path answered wrongly too. Being a 404 among 404s hides it
// properly.
func (p decoyProfile) serveNotFound(w http.ResponseWriter, r *http.Request) {
	body := fmt.Sprintf(p.notFound, p.server)

	w.Header().Set("Server", p.server)
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, body) // dropped by net/http for HEAD, as nginx does
}

// The default pages, as the matching nginx builds ship them. nginx 1.23.2 added
// the `color-scheme` rule to the index and dropped `bgcolor="white"` from the
// error pages, which is why there are two of each.

const indexClassic = `<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
<style>
    body {
        width: 35em;
        margin: 0 auto;
        font-family: Tahoma, Verdana, Arial, sans-serif;
    }
</style>
</head>
<body>
<h1>Welcome to nginx!</h1>
<p>If you see this page, the nginx web server is successfully installed and
working. Further configuration is required.</p>

<p>For online documentation and support please refer to
<a href="http://nginx.org/">nginx.org</a>.<br/>
Commercial support is available at
<a href="http://nginx.com/">nginx.com</a>.</p>

<p><em>Thank you for using nginx.</em></p>
</body>
</html>
`

const indexModern = `<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
<style>
html { color-scheme: light dark; }
body { width: 35em; margin: 0 auto;
font-family: Tahoma, Verdana, Arial, sans-serif; }
</style>
</head>
<body>
<h1>Welcome to nginx!</h1>
<p>If you see this page, the nginx web server is successfully installed and
working. Further configuration is required.</p>

<p>For online documentation and support please refer to
<a href="http://nginx.org/">nginx.org</a>.<br/>
Commercial support is available at
<a href="http://nginx.com/">nginx.com</a>.</p>

<p><em>Thank you for using nginx.</em></p>
</body>
</html>
`

const notFoundClassic = `<html>
<head><title>404 Not Found</title></head>
<body bgcolor="white">
<center><h1>404 Not Found</h1></center>
<hr><center>%s</center>
</body>
</html>
`

const notFoundModern = `<html>
<head><title>404 Not Found</title></head>
<body>
<center><h1>404 Not Found</h1></center>
<hr><center>%s</center>
</body>
</html>
`
