package tunnelspec

import "strings"

// TelegramHost is where the bot API lives.
const TelegramHost = "api.telegram.org:443"

// telegramPortSuffix marks the mapping that carries the bot.
var telegramPortSuffix = "=" + TelegramHost

// IsTelegramPort reports whether a mapping is the hidden Telegram forward.
func IsTelegramPort(p string) bool {
	return strings.HasSuffix(strings.TrimSpace(p), telegramPortSuffix)
}
