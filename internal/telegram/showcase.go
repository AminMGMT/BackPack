package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// The showcase, once a week in the bot.
//
// The same image and link the panel's overview shows (see manage/showcase.go)
// are sent to every admin of the bot on Friday, as the image with its link as
// a button under it. Friday in Tehran, from noon: the people reading this bot
// keep that week, and a message at three in the morning is one nobody sees.
// A Friday the monitor was not running for is skipped rather than made up on
// Saturday — a weekly message that turns up on the wrong day is a message out
// of its place.
//
// What has been delivered is kept on disk per recipient, so a restart does not
// send it twice and an admin whose delivery failed is tried again later the
// same Friday without the others receiving it again.

// showcaseSentFile records the last Friday sent and to whom; a variable so
// tests can point it somewhere writable.
var showcaseSentFile = app.ConfigDir + "/telegram-showcase.json"

// showcaseEvery is how often the loop looks at the clock. A send is due once a
// week; checking every quarter of an hour keeps it close to noon without
// costing anything in between.
const showcaseEvery = 15 * time.Minute

// showcaseHour is when on Friday the message becomes due, Tehran time.
const showcaseHour = 12

type showcaseSent struct {
	// Friday is the date of the Friday last sent, "2006-01-02".
	Friday string `json:"friday"`
	// To is every chat it reached that Friday.
	To []string `json:"to"`
}

// tehran is the clock the schedule is kept in. A fixed +03:30 stands in when
// the zone database is missing — Iran has had no daylight saving since 2022,
// so the two agree.
func tehran() *time.Location {
	if loc, err := time.LoadLocation("Asia/Tehran"); err == nil {
		return loc
	}
	return time.FixedZone("IRST", 3*3600+30*60)
}

// showcaseFriday returns the Friday the message is due for at now, or "" when
// now is not in the window.
func showcaseFriday(now time.Time) string {
	t := now.In(tehran())
	if t.Weekday() != time.Friday || t.Hour() < showcaseHour {
		return ""
	}
	return t.Format("2006-01-02")
}

// RunShowcase sends the weekly showcase until ctx is cancelled.
func RunShowcase(ctx context.Context) {
	for {
		if c := Load(); c.Token != "" && c.AdminID != "" {
			if friday := showcaseFriday(time.Now()); friday != "" {
				// The monitor keeps its own copy fresh: nobody may have opened
				// the panel this week to ask for one.
				_ = manage.RefreshShowcaseIfStale()
				_ = sendShowcase(c, friday)
			}
		}
		if !sleepCtx2(ctx, showcaseEvery) {
			return
		}
	}
}

// sendShowcase delivers this Friday's message to every admin not yet sent it.
func sendShowcase(c Config, friday string) error {
	var sent showcaseSent
	_ = app.LoadState(showcaseSentFile, &sent)
	if sent.Friday != friday {
		sent = showcaseSent{Friday: friday}
	}
	done := map[string]bool{}
	for _, id := range sent.To {
		done[id] = true
	}

	sc := manage.LoadShowcase()
	img, typ, _, err := manage.ShowcaseImage()
	if err != nil {
		return nil // nothing published, or nothing reached this server yet
	}

	// Uploaded once. Telegram answers with an id for the file, and every
	// other admin is sent that id instead of the same megabytes again — which
	// matters when each upload crosses the tunnel relay.
	var fileID string
	var lastErr error
	for _, id := range c.recipients() {
		if done[id] {
			continue
		}
		fid, err := postShowcase(c, id, sc, img, typ, fileID)
		if err != nil {
			lastErr = err
			continue
		}
		if fileID == "" {
			fileID = fid
		}
		sent.To = append(sent.To, id)
		data, _ := json.MarshalIndent(sent, "", "  ")
		_ = app.WriteFileAtomic(showcaseSentFile, data, 0600)
	}
	return lastErr
}

// showcaseMethod picks the Bot API method for an image: a GIF is an animation,
// so it plays in the chat; anything else is a photo.
func showcaseMethod(typ string) (method, field string) {
	if typ == "image/gif" {
		return "sendAnimation", "animation"
	}
	return "sendPhoto", "photo"
}

// showcaseExt names the upload by its type; Telegram reads the extension.
func showcaseExt(typ string) string {
	switch typ {
	case "image/gif":
		return ".gif"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	}
	return ".jpg"
}

// showcaseKeyboard is the link as a button, or nothing when there is no link.
func showcaseKeyboard(lang string, sc manage.Showcase) string {
	if sc.Link == "" {
		return ""
	}
	label := sc.Title
	if label == "" {
		label = tr(lang, "Open")
	}
	return kb(row(btn{Text: "🔗 " + label, URL: sc.Link}))
}

// postShowcase sends the image to one chat — the bytes, or a file id Telegram
// already holds — and returns the file id Telegram gives it.
func postShowcase(c Config, chatID string, sc manage.Showcase, img []byte, typ, fileID string) (string, error) {
	client, err := botClient(c, 120*time.Second)
	if err != nil {
		return "", err
	}
	method, field := showcaseMethod(typ)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", chatID)
	if sc.Title != "" {
		_ = w.WriteField("caption", b(sc.Title))
		_ = w.WriteField("parse_mode", parseMode)
	}
	if k := showcaseKeyboard(c.Language(), sc); k != "" {
		_ = w.WriteField("reply_markup", k)
	}
	if fileID != "" {
		_ = w.WriteField(field, fileID)
	} else {
		part, err := w.CreateFormFile(field, "showcase"+showcaseExt(typ))
		if err != nil {
			return "", err
		}
		if _, err := part.Write(img); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.Token, method), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New(redactToken(err.Error(), c.Token))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", describeStatus(resp)
	}

	var out struct {
		Result struct {
			Animation struct {
				FileID string `json:"file_id"`
			} `json:"animation"`
			Photo []struct {
				FileID string `json:"file_id"`
			} `json:"photo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// The message went out; only the file id to reuse is lost, so the
		// next admin gets the image uploaded again.
		return "", nil
	}
	if id := out.Result.Animation.FileID; id != "" {
		return id, nil
	}
	if p := out.Result.Photo; len(p) > 0 {
		// The sizes come smallest first; the last is the original.
		return p[len(p)-1].FileID, nil
	}
	return "", nil
}
