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
	"🔴 Tunnel <b>%s</b> is down":          "🔴 تونل <b>%s</b> قطع است",
	"🔴 Tunnel <b>%s</b> went down":        "🔴 تونل <b>%s</b> قطع شد",
	"🟢 Tunnel <b>%s</b> is back up":       "🟢 تونل <b>%s</b> دوباره وصل شد",
	"🗑 Tunnel <b>%s</b> no longer exists": "🗑 تونل <b>%s</b> دیگر وجود ندارد",

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
	"Address":                "آدرس",

	// The home menu
	"Overview": "وضعیت کلی",
	"Tunnels":  "تونل‌ها",
	"System":   "سیستم",
	"Alerts":   "هشدارها",
	"Health":   "سلامت",
	"History":  "تاریخچه",
	"Tools":    "ابزارها",
	"Support":  "حمایت",

	// Navigation
	"Back":            "بازگشت",
	"Refresh":         "به‌روزرسانی",
	"Cancel":          "لغو",
	"Confirm":         "تأیید",
	"Manage tunnels":  "مدیریت تونل‌ها",
	"OVERVIEW":        "وضعیت کلی",
	"Saved.":          "ذخیره شد.",
	"Not authorised.": "دسترسی ندارید.",
	"Measuring…":      "در حال اندازه‌گیری…",

	// The tunnel list and detail screen
	"Select your tunnel to manage.": "تونل موردنظر را برای مدیریت انتخاب کنید.",
	"%d total":                      "مجموع %d",
	"Restart all":                   "ری‌استارت همه",
	"Uptime 24h":                    "آپ‌تایم ۲۴ ساعت",
	"Peer":                          "طرف مقابل",
	"not connected":                 "متصل نیست",
	"connected":                     "متصل",
	"That tunnel no longer exists.": "این تونل دیگر وجود ندارد.",
	"Logs":                          "لاگ",
	"Start":                         "شروع",
	"Stop":                          "توقف",
	"Restart":                       "ری‌استارت",
	"Traffic":                       "ترافیک",
	"Ping":                          "پینگ",
	"No log output.":                "لاگی ثبت نشده است.",

	// Health details, as the health checker words them
	"peer connected":         "طرف مقابل متصل است",
	"service is not running": "سرویس در حال اجرا نیست",
	"no systemd unit — the tunnel is not installed": "یونیت systemd وجود ندارد — تونل نصب نشده است",
	"running, but no client is connected yet":       "در حال اجراست، ولی هنوز کلاینتی وصل نشده",
	"running, but not connected to the server":      "در حال اجراست، ولی به سرور وصل نیست",

	// Actions and their confirmations
	"Stop tunnel %s?":         "تونل %s متوقف شود؟",
	"Restart tunnel %s?":      "تونل %s ری‌استارت شود؟",
	"Restart all %d tunnels?": "هر %d تونل ری‌استارت شوند؟",
	"Yes, stop it":            "بله، متوقف کن",
	"Yes, restart it":         "بله، ری‌استارت کن",
	"Yes, restart all":        "بله، همه را ری‌استارت کن",
	"Everything it carries drops until it is started again.": "هرچه از آن عبور می‌کند تا زمان شروع دوباره قطع می‌شود.",
	"Connections through it drop and reconnect.":             "اتصال‌های روی آن قطع و دوباره وصل می‌شوند.",
	"Every connection drops and reconnects.":                 "همه اتصال‌ها قطع و دوباره وصل می‌شوند.",
	"▶️ %s started":                                          "▶️ %s شروع شد",
	"⏹ %s stopped":                                           "⏹ %s متوقف شد",
	"🔄 %s restarted":                                         "🔄 %s ری‌استارت شد",
	"🔄 %d tunnels restarted":                                 "🔄 %d تونل ری‌استارت شد",
	"🔄 %d restarted, %d failed":                              "🔄 %d ری‌استارت شد، %d ناموفق",
	"Too quick — try again in %ds.":                          "کمی سریع بود — %d ثانیه دیگر دوباره تلاش کنید.",
	"That confirmation has expired — please try again.":      "این تأیید منقضی شده — دوباره تلاش کنید.",
	"Your access is read-only.":                              "دسترسی شما فقط خواندنی است.",

	// Traffic
	"Last 24 hours": "۲۴ ساعت گذشته",
	"Busiest hour":  "پرترافیک‌ترین ساعت",
	"Total":         "مجموع",
	"Not enough history yet — the sampler needs about an hour.": "هنوز تاریخچه کافی نیست — حدود یک ساعت زمان لازم است.",

	// Ping
	"Target":   "مقصد",
	"Latency":  "تأخیر",
	"Jitter":   "جیتر",
	"Loss":     "اتلاف",
	"no reply": "پاسخی دریافت نشد",
	"No peer address to measure — the tunnel is not connected.": "آدرسی برای اندازه‌گیری نیست — تونل متصل نیست.",

	// Alerts screen
	"Alerts are on":  "هشدارها روشن است",
	"Alerts are off": "هشدارها خاموش است",
	"Nothing will be reported until they are switched back on.": "تا وقتی دوباره روشن نشوند چیزی گزارش نمی‌شود.",
	"Tunnel up/down": "قطع و وصل تونل",
	"New release":    "نسخه جدید",
	"Checked every %ds, repeated at most every %dm.": "هر %d ثانیه بررسی می‌شود و حداکثر هر %d دقیقه تکرار می‌شود.",
	"above %d%%": "بالای %d%%",
	"on":         "روشن",
	"off":        "خاموش",
	"Turn on":    "روشن کن",
	"Turn off":   "خاموش کن",
	"Tunnel":     "تونل",
	"Release":    "نسخه",

	// Tools, health and updating
	"Backup":                      "پشتیبان",
	"Update":                      "به‌روزرسانی",
	"Restore points":              "نقاط بازیابی",
	"Relay check":                 "بررسی رله",
	"Route":                       "مسیر",
	"Everything checks out.":      "همه‌چیز سالم است.",
	"Installed":                   "نصب‌شده",
	"Available":                   "در دسترس",
	"This is the newest release.": "این جدیدترین نسخه است.",
	"Version %s is available (you are on %s).":                                                     "نسخه %s در دسترس است (نسخه فعلی شما %s است).",
	"A restore point is saved first, and it rolls itself back if the tunnels do not come back up.": "ابتدا یک نقطه بازیابی ذخیره می‌شود و اگر تونل‌ها بالا نیامدند، خودش برمی‌گردد.",
	"Update now":    "الان به‌روزرسانی کن",
	"Update to %s?": "به %s به‌روزرسانی شود؟",
	"The tunnels restart as part of it. A restore point is saved first.": "تونل‌ها در این فرایند ری‌استارت می‌شوند. ابتدا یک نقطه بازیابی ذخیره می‌شود.",
	"Yes, update":                     "بله، به‌روزرسانی کن",
	"Updating — this takes a minute.": "در حال به‌روزرسانی — حدود یک دقیقه طول می‌کشد.",
	"⬆️ Update started":               "⬆️ به‌روزرسانی شروع شد",
	"Update finished.":                "به‌روزرسانی تمام شد.",
	"Update failed.":                  "به‌روزرسانی ناموفق بود.",
	"🔐 Preparing your backup…":        "🔐 در حال آماده‌سازی پشتیبان…",
	"Backup failed":                   "ساخت پشتیبان ناموفق بود",

	// Restore points
	"None saved yet. One is taken automatically before every update.":                        "هنوز چیزی ذخیره نشده. قبل از هر به‌روزرسانی یکی خودکار ساخته می‌شود.",
	"Restoring replaces the binary and every config with the saved copy.":                    "بازیابی، فایل اجرایی و همه تنظیمات را با نسخه ذخیره‌شده جایگزین می‌کند.",
	"That restore point is gone.":                                                            "این نقطه بازیابی دیگر وجود ندارد.",
	"Roll back to %s?":                                                                       "به %s برگردانده شود؟",
	"The binary and every config are replaced with the saved copy, and the tunnels restart.": "فایل اجرایی و همه تنظیمات با نسخه ذخیره‌شده جایگزین می‌شوند و تونل‌ها ری‌استارت می‌شوند.",
	"Yes, roll back":                      "بله، برگردان",
	"Rolling back — this takes a minute.": "در حال بازگردانی — حدود یک دقیقه طول می‌کشد.",
	"♻️ Rollback started":                 "♻️ بازگردانی شروع شد",
	"Rollback finished.":                  "بازگردانی تمام شد.",
	"Rollback failed.":                    "بازگردانی ناموفق بود.",

	// History
	"Alert history":                  "تاریخچه هشدارها",
	"Bot actions":                    "عملیات ربات",
	"Firing now":                     "هم‌اکنون فعال",
	"Nothing has been reported yet.": "هنوز چیزی گزارش نشده است.",
	"No actions have been taken through the bot yet.": "هنوز هیچ عملیاتی از طریق ربات انجام نشده است.",

	// The slash-command menu and help screen
	"every tunnel: state, ports, traffic": "همه تونل‌ها: وضعیت، پورت و ترافیک",
	"start, stop or restart a tunnel":     "شروع، توقف یا ری‌استارت یک تونل",
	"processor, memory and disk":          "پردازنده، حافظه و دیسک",
	"alert thresholds and switches":       "آستانه‌ها و کلیدهای هشدار",
	"run every diagnostic check":          "اجرای همه بررسی‌های تشخیصی",
	"recent alerts and bot actions":       "هشدارها و عملیات اخیر ربات",
	"send a full backup here as a file":   "ارسال پشتیبان کامل به‌صورت فایل",
	"panel link and login code":           "لینک پنل و رمز ورود",
	"project links and donations":         "لینک‌های پروژه و حمایت مالی",
	"what the bot can do":                 "ربات چه کارهایی می‌تواند بکند",
	"Alerts arrive on their own when a threshold is crossed, a tunnel changes state, or a new version is released.": "هشدارها خودبه‌خود می‌آیند: وقتی آستانه‌ای رد شود، تونلی قطع یا وصل شود، یا نسخه جدیدی منتشر شود.",
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
