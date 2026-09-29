// Package geo resolves an IP address to a country, city and network operator.
//
// It lives on its own because both the web panel and the Telegram bot need it,
// and the panel imports the bot — so the bot cannot reach back into the panel
// for it. Sharing the lookup also shares the cache, which matters: these are
// free public APIs with rate limits, and two components asking the same
// question separately is the way to get throttled.
package geo

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Info is what is known about an address.
type Info struct {
	Status  string `json:"status"`
	Country string `json:"country"`
	// Code is the ISO 3166-1 alpha-2 country code, which is what a flag emoji
	// is built from. The providers all return the country name too, but a name
	// cannot be turned into a flag.
	Code string `json:"countryCode"`
	City string `json:"city"`
	ISP  string `json:"isp"`
}

type geoEntry struct {
	info *Info
	at   time.Time
}

var (
	geoCache = map[string]geoEntry{}
	// geoMiss is when a lookup last found nothing. Without it an address the
	// providers cannot answer for — every address, on a server that cannot
	// reach them, which an Iran server usually cannot — was asked about again
	// on every call, three providers at up to six seconds each, and the web
	// panel's tunnel list waited for all of it on every poll.
	geoMiss  = map[string]time.Time{}
	geoBusy  = map[string]bool{}
	geoMu    sync.Mutex
	missTTL  = 10 * time.Minute
	foundTTL = 6 * time.Hour
)

// geoProviders is an ordered list of lookup functions. The first that succeeds
// wins. Multiple providers are tried because any single one (e.g. ip-api.com)
// may be blocked from some networks such as Iran.
var geoProviders = []func(string) *Info{geoFromIPApi, geoFromIpwho, geoFromIpSb}

// Lookup resolves an IP, caching the answer. It returns nil when nothing is
// known, which callers must treat as "unavailable" rather than an error.
func Lookup(ip string) *Info {
	if ip == "" || ip == "-" {
		return nil
	}
	if g, fresh := cached(ip); fresh {
		return g
	}
	return lookup(ip)
}

// Peek is Lookup for a caller that must not wait on the network: it answers
// from the cache at once — nil when nothing is known yet — and, when the
// answer is missing or old, asks the providers in the background so a later
// call has it. One question per address is in flight at a time.
func Peek(ip string) *Info {
	if ip == "" || ip == "-" {
		return nil
	}
	g, fresh := cached(ip)
	if fresh {
		return g
	}
	geoMu.Lock()
	if !geoBusy[ip] {
		geoBusy[ip] = true
		go func() {
			lookup(ip)
			geoMu.Lock()
			delete(geoBusy, ip)
			geoMu.Unlock()
		}()
	}
	geoMu.Unlock()
	return g // an old answer is better than none while the new one comes
}

// cached is what the cache holds for ip, and whether it is still fresh — a
// recent failure counting as a fresh "nothing".
func cached(ip string) (*Info, bool) {
	geoMu.Lock()
	defer geoMu.Unlock()
	e, ok := geoCache[ip]
	if ok && time.Since(e.at) < foundTTL {
		return e.info, true
	}
	if at, missed := geoMiss[ip]; missed && time.Since(at) < missTTL {
		return e.info, true
	}
	return e.info, false
}

// lookup asks the providers in turn and records the answer, or the miss.
func lookup(ip string) *Info {
	for _, provider := range geoProviders {
		if g := provider(ip); g != nil && (g.Country != "" || g.ISP != "") {
			geoMu.Lock()
			geoCache[ip] = geoEntry{info: g, at: time.Now()}
			delete(geoMiss, ip)
			geoMu.Unlock()
			return g
		}
	}
	geoMu.Lock()
	geoMiss[ip] = time.Now()
	geoMu.Unlock()
	return nil
}

func geoGet(url string, out any) bool {
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// geoFromIPApi — ip-api.com (HTTP, no key).
func geoFromIPApi(ip string) *Info {
	var g Info
	if geoGet("http://ip-api.com/json/"+ip+"?fields=status,country,countryCode,city,isp", &g) && g.Status == "success" {
		return &g
	}
	return nil
}

// geoFromIpwho — ipwho.is (HTTPS, no key).
func geoFromIpwho(ip string) *Info {
	var r struct {
		Success     bool   `json:"success"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		City        string `json:"city"`
		Connection  struct {
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if geoGet("https://ipwho.is/"+ip, &r) && r.Success {
		isp := r.Connection.ISP
		if isp == "" {
			isp = r.Connection.Org
		}
		return &Info{Country: r.Country, Code: r.CountryCode, City: r.City, ISP: isp}
	}
	return nil
}

// geoFromIpSb — api.ip.sb (HTTPS, no key).
func geoFromIpSb(ip string) *Info {
	var r struct {
		Country      string `json:"country"`
		CountryCode  string `json:"country_code"`
		City         string `json:"city"`
		ISP          string `json:"isp"`
		Organization string `json:"organization"`
	}
	if geoGet("https://api.ip.sb/geoip/"+ip, &r) && r.Country != "" {
		isp := r.ISP
		if isp == "" {
			isp = r.Organization
		}
		return &Info{Country: r.Country, Code: r.CountryCode, City: r.City, ISP: isp}
	}
	return nil
}
