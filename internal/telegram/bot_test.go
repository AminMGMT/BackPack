package telegram

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/tunhist"
)

// A tunnel's handle must survive a restart, because the buttons in the chat do.
// An index into the list would not: adding a tunnel renumbers everything after
// it, and yesterday's Stop button would then point at a different tunnel.
func TestTunnelIDIsStableAndDistinct(t *testing.T) {
	if tunnelID("germany") != tunnelID("germany") {
		t.Error("the same name must always produce the same handle")
	}
	seen := map[string]string{}
	for _, name := range []string{"germany", "turkey", "sweden", "de1", "de2", "a", ""} {
		id := tunnelID(name)
		if prev, clash := seen[id]; clash {
			t.Errorf("%q and %q share the handle %q", name, prev, id)
		}
		seen[id] = name
	}
	// The handle plus the longest action verb must fit Telegram's 64-byte cap
	// on callback data, which is the reason it is a hash at all.
	if got := len("act:restart:" + tunnelID("germany")); got > 64 {
		t.Errorf("callback data is %d bytes, over Telegram's 64", got)
	}
}

// A confirmation must not stay live in the chat forever: scrolling back to a
// three-day-old message should not be a way to stop a tunnel.
func TestConfirmationExpires(t *testing.T) {
	now := time.Now()
	tok := confirmToken(now)

	if !confirmFresh(tok, now) {
		t.Error("a confirmation offered now must be accepted")
	}
	if !confirmFresh(tok, now.Add(confirmTTL-time.Second)) {
		t.Error("a confirmation inside its window must be accepted")
	}
	if confirmFresh(tok, now.Add(confirmTTL+time.Second)) {
		t.Error("a confirmation past its window must be refused")
	}
	if confirmFresh(tok, now.Add(72*time.Hour)) {
		t.Error("a three-day-old button must not still work")
	}
	// A token that cannot be read has to fall on the safe side.
	if confirmFresh("not-a-token", now) {
		t.Error("an unreadable token must be treated as expired")
	}
}

// The rate limit exists so a phone in a pocket cannot restart a tunnel eight
// times in four seconds.
func TestRateLimit(t *testing.T) {
	l := &limiter{last: map[string]time.Time{}, recent: map[string][]time.Time{}}
	now := time.Now()

	if ok, _ := l.allow("1", "restart:de", now); !ok {
		t.Fatal("the first press must go through")
	}
	if ok, wait := l.allow("1", "restart:de", now.Add(time.Second)); ok {
		t.Error("a second press one second later must be refused")
	} else if wait <= 0 {
		t.Error("a refusal must say how long to wait")
	}
	if ok, _ := l.allow("1", "restart:de", now.Add(actionCooldown+time.Second)); !ok {
		t.Error("the press must go through once the cooldown has passed")
	}
	// A different tunnel is a different action and must not be blocked by the
	// first one's cooldown.
	if ok, _ := l.allow("1", "restart:nl", now.Add(time.Second)); !ok {
		t.Error("another tunnel must not share the first one's cooldown")
	}

	// The per-user budget catches the case the cooldown cannot: working through
	// every tunnel at once.
	l2 := &limiter{last: map[string]time.Time{}, recent: map[string][]time.Time{}}
	for i := 0; i < actionBudget; i++ {
		if ok, _ := l2.allow("2", "restart:t"+string(rune('a'+i)), now); !ok {
			t.Fatalf("press %d was refused before the budget was spent", i+1)
		}
	}
	if ok, _ := l2.allow("2", "restart:zz", now); ok {
		t.Error("the budget must stop the press after it is spent")
	}
}

// The owner can always act; a read-only admin never can; a stranger is neither.
func TestAdminPermissions(t *testing.T) {
	c := Config{AdminID: "1", Admins: []Admin{
		{ID: "2"}, {ID: "3", ReadOnly: true},
	}}

	for _, tc := range []struct {
		id             string
		admin, canEdit bool
	}{
		{"1", true, true},   // owner
		{"2", true, true},   // full admin
		{"3", true, false},  // read-only admin
		{"4", false, false}, // stranger
		{"", false, false},
	} {
		if got := c.isAdmin(tc.id); got != tc.admin {
			t.Errorf("isAdmin(%q) = %v, want %v", tc.id, got, tc.admin)
		}
		if got := c.canWrite(tc.id); got != tc.canEdit {
			t.Errorf("canWrite(%q) = %v, want %v", tc.id, got, tc.canEdit)
		}
	}

	// Listing the owner as read-only must not lock them out — a mis-saved
	// config should not leave a server nobody can drive.
	locked := Config{AdminID: "1", Admins: []Admin{{ID: "1", ReadOnly: true}}}
	if !locked.canWrite("1") {
		t.Error("the owner must keep write access however the list is written")
	}

	// Alerts go to everyone, owner first and without duplicates.
	got := c.recipients()
	if want := []string{"1", "2", "3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("recipients() = %v, want %v", got, want)
	}
	if n := len(locked.recipients()); n != 1 {
		t.Errorf("the owner listed twice must appear once, got %d recipients", n)
	}
}

func TestParseAdmins(t *testing.T) {
	got := ParseAdmins("111, 222:ro\n333; 444:RO, notanid, 111")
	want := []Admin{
		{ID: "111"}, {ID: "222", ReadOnly: true},
		{ID: "333"}, {ID: "444", ReadOnly: true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d admins, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("admin %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Counters restart from zero when a tunnel's process does. Treating that as
// negative traffic (or as zero) is how a busy hour disappears from the chart.
func TestHourlyDeltasSurviveACounterReset(t *testing.T) {
	buckets := []tunhist.Hour{
		{In: 100, Out: 100}, // 200 cumulative
		{In: 300, Out: 300}, // 600 → 400 this hour
		{In: 50, Out: 50},   // 100 → restarted, so 100 this hour
		{In: 80, Out: 80},   // 160 → 60 this hour
	}
	deltas, peak, total := hourlyDeltas(buckets, 24)

	want := []uint64{400, 100, 60}
	if len(deltas) != len(want) {
		t.Fatalf("got %d deltas, want %d: %v", len(deltas), len(want), deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta %d = %d, want %d", i, deltas[i], want[i])
		}
	}
	if peak != 400 {
		t.Errorf("peak = %d, want 400", peak)
	}
	if total != 560 {
		t.Errorf("total = %d, want 560", total)
	}
}

func TestSparkline(t *testing.T) {
	got := sparkline([]uint64{0, 50, 100}, 100)
	if []rune(got)[0] != sparkChars[0] {
		t.Errorf("the lowest value must draw the lowest block, got %q", got)
	}
	if []rune(got)[2] != sparkChars[len(sparkChars)-1] {
		t.Errorf("the peak must draw the tallest block, got %q", got)
	}
	// No traffic is an answer; a blank line looks like a broken screen.
	if flat := sparkline([]uint64{0, 0, 0}, 0); flat != strings.Repeat(string(sparkChars[0]), 3) {
		t.Errorf("an idle series must still draw a floor, got %q", flat)
	}
}

// Every message goes out as HTML, so anything that reaches a message body and
// came from outside — a tunnel name, a log line, an error — has to be escaped
// or Telegram rejects the whole message.
func TestEscaping(t *testing.T) {
	if got := esc(`a<b>&"c"`); got != `a&lt;b&gt;&amp;"c"` {
		t.Errorf("esc produced %q", got)
	}
	// Round-tripping must give the original back, which is what makes the
	// plain-text fallback in clampText safe.
	original := `tunnel <de1> & "friends"`
	if got := stripTags(esc(original)); got != original {
		t.Errorf("stripTags(esc(%q)) = %q", original, got)
	}
	// Tags are removed, not escaped, so the fallback carries no markup at all.
	if got := stripTags("<b>bold</b> text"); got != "bold text" {
		t.Errorf("stripTags left markup behind: %q", got)
	}
}

// The log screen is the one that overflows, and a body cut mid-tag is a message
// Telegram refuses outright.
func TestLongOutputStaysSendable(t *testing.T) {
	huge := strings.Repeat("a line of journal output that goes on and on\n", 500)
	body := b("📄 Logs") + "\n\n" + preBlock(huge)

	if len(body) > maxMessage {
		t.Errorf("body is %d bytes, over the %d limit", len(body), maxMessage)
	}
	if strings.Count(body, "<pre>") != 1 || strings.Count(body, "</pre>") != 1 {
		t.Errorf("the monospace block is not balanced:\n%s", body[:200])
	}
	if got := clampText(body); got != body {
		t.Error("a body already inside the limit must be sent unchanged")
	}

	// And when the guard does fire, what it produces must still be sendable.
	overflowing := "<b>" + strings.Repeat("x", maxMessage*2) + "</b>"
	clamped := clampText(overflowing)
	if len(clamped) > maxMessage {
		t.Errorf("clamped body is still %d bytes", len(clamped))
	}
	if strings.ContainsAny(clamped, "<>") {
		t.Errorf("the fallback must carry no markup: %q", clamped[:60])
	}
}

// Switching a check off should take a deliberate walk through the ladder, not
// one stray tap.
func TestThresholdLadder(t *testing.T) {
	seen := map[int]bool{}
	v := thresholdLadder[0]
	for i := 0; i < len(thresholdLadder); i++ {
		if seen[v] {
			t.Fatalf("the ladder repeats %d before completing a cycle", v)
		}
		seen[v] = true
		v = nextThreshold(v)
	}
	if v != thresholdLadder[0] {
		t.Errorf("the ladder does not close the loop, ended at %d", v)
	}
	if !seen[0] {
		t.Error("the ladder must include off")
	}
	// Anything unrecognised — a value set by an older build or hand-edited —
	// has to land somewhere sensible rather than nowhere.
	if got := nextThreshold(42); got != thresholdLadder[0] {
		t.Errorf("nextThreshold(42) = %d, want %d", got, thresholdLadder[0])
	}
}

// The offset is what stops a button press being replayed after a crash, so it
// has to survive a round trip through the disk.
func TestOffsetPersists(t *testing.T) {
	dir := t.TempDir()
	saved := offsetFile
	offsetFile = filepath.Join(dir, "telegram-offset")
	defer func() { offsetFile = saved }()

	if got := loadOffset(); got != 0 {
		t.Errorf("a missing file must read as 0, got %d", got)
	}
	saveOffset(987654321)
	if got := loadOffset(); got != 987654321 {
		t.Errorf("loadOffset() = %d, want 987654321", got)
	}
	// A corrupted file must not wedge the bot; starting over is the safe answer.
	if err := os.WriteFile(offsetFile, []byte("nonsense"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadOffset(); got != 0 {
		t.Errorf("an unreadable offset must fall back to 0, got %d", got)
	}
}

// Persian is a supported language, not a partial one: a sentence the bot can
// write with no translation behind it silently sends in English, which is how a
// half-translated bot happens.
func TestPersianCoversEverySentence(t *testing.T) {
	pattern := regexp.MustCompile(`tr\(\s*(?:lang|w\.lang)\s*,\s*"((?:[^"\\]|\\.)*)"`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "lang.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("cannot read %s: %v", f, err)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			// The match is source text, so "\n" is still two characters here
			// while the translation table is keyed by the real string.
			sentence, err := strconv.Unquote(`"` + m[1] + `"`)
			if err != nil {
				t.Fatalf("%s: cannot read the literal %q: %v", f, m[1], err)
			}
			if tr(LangFA, sentence) == sentence {
				t.Errorf("%s: no Persian for %q", f, sentence)
			}
		}
	}
}
