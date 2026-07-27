package telegram

// What the bot writes, in the operator's language.
//
// The key of a translation is the English sentence itself. It costs a little
// duplication and buys the property that matters most here: anything without a
// translation still sends, in English, rather than sending a key or nothing at
// all. A half-finished translation degrades to the old wording instead of to a
// broken message — and these are the messages that say a tunnel went down, so
// sending nothing would be far worse than sending the wrong language.
//
// An empty language means English, which is what every config written before
// this existed says. Updating therefore never changes the language of a bot
// somebody was already reading.

// LangEN and LangFA are the languages the bot can write in.
const (
	LangEN = "en"
	LangFA = "fa"
)

// fa holds the Persian wording. Entries are whole sentences, including their
// format verbs: a message stitched together from translated fragments comes out
// as nonsense in a language whose word order is not English's, and keeping the
// verbs inside the sentence lets the translation put them where they belong.
var fa = map[string]string{
	// Threshold alerts
	"⚠️ %s at %.1f%% (threshold %d%%)\n%s": "⚠️ %s روی %.1f%% است (آستانه %d%%)\n%s",
	"⚠️ %s still at %.1f%%\n%s":            "⚠️ %s همچنان روی %.1f%% است\n%s",
	"✅ %s back to normal — %.1f%%":         "✅ %s به حالت عادی برگشت — %.1f%%",
	"Processor":                            "پردازنده",
	"Memory":                               "حافظه",
	"Disk":                                 "دیسک",

	// Tunnel transitions
	"🔴 Tunnel *%s* is down":          "🔴 تونل *%s* قطع است",
	"🔴 Tunnel *%s* went down":        "🔴 تونل *%s* قطع شد",
	"🟢 Tunnel *%s* is back up":       "🟢 تونل *%s* دوباره وصل شد",
	"🗑 Tunnel *%s* no longer exists": "🗑 تونل *%s* دیگر وجود ندارد",

	// Release announcement
	"⬆️ Backpack %s has been released (you are on %s).":                                            "⬆️ نسخه %s بک‌پک منتشر شد (نسخه فعلی شما %s است).",
	"Update from the CLI: sudo backpack → Update.":                                                 "برای به‌روزرسانی در ترمینال: sudo backpack ← Update.",
	"It saves a restore point first and rolls back by itself if the tunnel does not come back up.": "ابتدا یک نقطه بازیابی می‌سازد و اگر تونل بالا نیامد، خودش برمی‌گردد.",
	// Status report
	"No tunnels configured.": "هیچ تونلی تنظیم نشده است.",
	"Tunnel Port":            "پورت تونل",
	"Forwarded Port":         "پورت فورواردشده",
	"Server":                 "سرور",
	"Web Panel":              "پنل وب",
	"Password":               "رمز عبور",
}

// tr translates one sentence, returning it unchanged when there is no entry.
func tr(lang, s string) string {
	if lang != LangFA {
		return s
	}
	if out, ok := fa[s]; ok {
		return out
	}
	return s
}

// normaliseLang maps anything unrecognised — including the empty value every
// config written before this field existed carries — onto English.
func normaliseLang(lang string) string {
	if lang == LangFA {
		return LangFA
	}
	return LangEN
}

// Language reports the language the bot should write in.
func (c Config) Language() string { return normaliseLang(c.Lang) }
