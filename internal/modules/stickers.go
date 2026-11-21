package modules

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Sticker tools (modules/stickers.py): /getsticker and /kang.

const (
	packLimit    = 120
	maxPacks     = 20
	kangCooldown = 5 * time.Second
	defaultEmoji = "🫡"
	emojiEnough  = 20 // the longest emoji argument worth looking at
)

var errNoRoom = errors.New("no room")

var stickerKinds = map[string]string{"static": "s", "animated": "a", "video": "v"}

func registerStickers(r *core.Router) {
	r.Command(cmdGetSticker, "getsticker")
	r.Command(cmdKang, "kang")
}

func stickerKind(s *gotgbot.Sticker) string {
	switch {
	case s.IsAnimated:
		return "animated"
	case s.IsVideo:
		return "video"
	}
	return "static"
}

func packName(userID int64, kind string, number int, bot string) string {
	return fmt.Sprintf("%s%d_%d_by_%s", stickerKinds[kind], userID, number, bot)
}

// packTitle is like "Ann's sticker kang pack #1" (Telegram allows 64 characters).
func packTitle(c *core.Ctx, u *gotgbot.User, kind string, number int) string {
	name := strings.TrimSpace(u.FirstName)
	if name == "" {
		name = c.T("stickers.my")
	}
	tail := c.T("stickers.s_kang_pack", core.V{"v": c.T("stickers.title_kind." + kind), "number": number})
	r := []rune(name)
	if keep := 64 - len([]rune(tail)); keep < len(r) {
		if keep < 0 {
			keep = 0
		}
		r = r[:keep]
	}
	return string(r) + tail
}

func cmdGetSticker(c *core.Ctx) error {
	replied := core.RepliedMessage(c.Msg)
	if replied == nil || replied.Sticker == nil {
		c.Say(c.T("stickers.reply_to_the_sticker_you_want"))
		return nil
	}
	s := replied.Sticker
	err := func() error {
		data, err := c.Download(s.FileId)
		if err != nil {
			return err
		}
		kind := stickerKind(s)
		name := s.FileUniqueId + ".webm"
		switch kind {
		case "static":
			name = s.FileUniqueId + ".png"
			if data, err = core.StillPNG(data, false); err != nil {
				return err
			}
		case "animated":
			name = s.FileUniqueId + ".tgs"
		}
		opts := &gotgbot.SendDocumentOpts{
			ReplyParameters: &gotgbot.ReplyParameters{MessageId: c.Msg.MessageId, AllowSendingWithoutReply: true},
		}
		if c.Msg.IsTopicMessage {
			opts.MessageThreadId = c.Msg.MessageThreadId
		}
		_, err = c.Bot.SendDocumentWithContext(c.Ctx, c.Msg.Chat.Id, gotgbot.InputFileByReader(name, bytes.NewReader(data)), opts)
		return err
	}()
	if err != nil { // a broken file or Telegram refusing it: say so instead of staying silent
		slog.Warn("getsticker failed", "err", err)
		c.Say(c.T("stickers.i_couldn_t_turn_that_sticker"))
	}
	return nil
}

// emojiOf is the emoji for the new sticker: the first word typed that is not plain text, else the sticker's own.
func emojiOf(args string, s *gotgbot.Sticker) string {
	if f := strings.Fields(args); len(f) > 0 {
		typed := f[0]
		plain := true
		for _, r := range typed {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
				plain = false
				break
			}
		}
		if len([]rune(typed)) <= emojiEnough && !plain {
			return typed
		}
	}
	if s != nil && s.Emoji != "" {
		return s.Emoji
	}
	return defaultEmoji
}

// findPack is the name, number and existence of the pack of this kind that has room for one more sticker.
func findPack(c *core.Ctx, userID int64, kind string) (string, int, bool, error) {
	for n := 1; n <= maxPacks; n++ {
		name := packName(userID, kind, n, c.Bot.Username)
		set, err := c.Bot.GetStickerSetWithContext(c.Ctx, name, nil)
		if err != nil {
			if tg := core.TgError(err); tg != nil && tg.Code == 400 {
				return name, n, false, nil
			}
			return "", 0, false, err
		}
		if len(set.Stickers) < packLimit {
			return name, n, true, nil
		}
	}
	return "", 0, false, errNoRoom
}

func refusal(c *core.Ctx, err error) string {
	msg := err.Error()
	if tg := core.TgError(err); tg != nil {
		msg = tg.Description
	}
	if i := strings.Index(msg, ":"); i >= 0 {
		msg = msg[i+1:]
	}
	if msg = strings.TrimSpace(msg); msg == "" {
		return c.T("stickers.unknown_error")
	}
	return msg
}

func cmdKang(c *core.Ctx) error {
	user := c.Msg.From
	replied := core.RepliedMessage(c.Msg)
	if user == nil || c.Msg.SenderChat != nil {
		c.Say(c.T("stickers.i_need_to_know_who_you"))
		return nil
	}
	var sticker *gotgbot.Sticker
	var photo []gotgbot.PhotoSize
	if replied != nil {
		sticker, photo = replied.Sticker, replied.Photo
	}
	if sticker == nil && len(photo) == 0 {
		c.Say(c.T("stickers.reply_to_a_sticker_or_a"))
		return nil
	}
	key := fmt.Sprintf("kang:%d", user.Id)
	if !c.App.Cooldown(key, kangCooldown) {
		c.Say(c.T("stickers.slow_down_one_sticker_at_a"))
		return nil
	}
	emoji := emojiOf(c.Args, sticker)

	kind := "static"
	var source gotgbot.InputFileOrString
	var perr error
	if sticker != nil {
		kind = stickerKind(sticker)
		source = gotgbot.InputFileByID(sticker.FileId)
	} else {
		var data []byte
		if data, perr = c.Download(photo[len(photo)-1].FileId); perr == nil {
			data, perr = core.StillPNG(data, true)
		}
		source = gotgbot.InputFileByReader("sticker.png", bytes.NewReader(data))
	}
	if perr != nil {
		c.App.ForgetCooldown(key)
		slog.Warn("kang failed", "user", user.Id, "err", perr)
		c.Say(c.T("stickers.i_couldn_t_use_that_picture"))
		return nil
	}
	name, number, exists, perr := findPack(c, user.Id, kind)
	if perr == nil {
		item := gotgbot.InputSticker{Sticker: source, Format: kind, EmojiList: []string{emoji}}
		if exists {
			_, perr = c.Bot.AddStickerToSetWithContext(c.Ctx, user.Id, name, item, nil)
		} else {
			_, perr = c.Bot.CreateNewStickerSetWithContext(c.Ctx, user.Id, name, packTitle(c, user, kind, number), gotgbot.InputStickers{item}, nil)
		}
	}
	if perr != nil {
		if errors.Is(perr, errNoRoom) {
			c.Say(c.T("stickers.all_your_packs_are_full_delete"))
			return nil
		}
		c.App.ForgetCooldown(key)
		slog.Warn("kang failed", "user", user.Id, "err", perr)
		if core.TgError(perr) != nil {
			c.Say(c.T("stickers.telegram_refused_it", core.V{"reason": text.Esc(refusal(c, perr))}))
		} else {
			c.Say(c.T("stickers.i_couldn_t_use_that_picture"))
		}
		return nil
	}
	markup := &gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
		{Text: c.T("stickers.open_the_pack"), Url: "https://t.me/addstickers/" + name},
	}}}
	tag := "stickers.sticker_added_to_your_new_pack"
	if exists {
		tag = "stickers.sticker_added_to_your_pack"
	}
	_, e := c.Reply(c.T(tag, core.V{"emoji": text.Esc(emoji)}), markup)
	return e
}
