package core

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Finding out which user a command is aimed at (core/targets.py in the Python version).
//
// Command syntax, for every command that acts on a user:
//
//	/command <user> [extra data]
//
// The user always comes first. It can be given as a text mention (the person's name as a clickable link), a
// @username mention or an id typed by hand, or nothing at all when the command is a reply to the person's
// message; then everything after the command is extra data. Only the first argument is read as the user, so
// mentions inside the extra data (a reason, a note) are never mistaken for the target.

// TargetError carries a message that is safe to show to the user (plain text).
type TargetError struct {
	Msg     string
	Unknown bool // a @username nobody has ever been seen with
}

func (e *TargetError) Error() string { return e.Msg }

// Target is whoever (or whatever) a command is aimed at.
type Target struct {
	ID        int64 // the id of the user, or of the chat when Kind is a chat type
	Name      string
	Extra     string // everything after the user: a reason, a duration, ...
	Kind      string // user, bot, group, supergroup or channel
	FirstName string
	LastName  string
	Username  string
	Title     string
}

// IsKnown: whether anything but the id is known (an id nobody has ever seen has no name).
func (t *Target) IsKnown() bool {
	return t.IsChat() || t.Name != "" || t.FirstName != "" || t.Username != ""
}

func (t *Target) IsChat() bool {
	return t.Kind == "group" || t.Kind == "supergroup" || t.Kind == "channel"
}

var (
	usernameRE = regexp.MustCompile(`^@?([A-Za-z][A-Za-z0-9_]{3,31})$`)
	idRE       = regexp.MustCompile(`^-?\d+$`)
)

func userKind(isBot bool, username string) string {
	// Only bots can have a username ending in "bot".
	if isBot || strings.HasSuffix(strings.ToLower(username), "bot") {
		return "bot"
	}
	return "user"
}

func targetFromUser(u *gotgbot.User) *Target {
	return &Target{ID: u.Id, Name: FullName(u), Kind: userKind(u.IsBot, u.Username),
		FirstName: u.FirstName, LastName: u.LastName, Username: u.Username}
}

func targetFromRow(r *UserRow) *Target {
	return &Target{ID: r.ID, Name: r.Name, Kind: userKind(r.IsBot, r.Username),
		FirstName: r.FirstName, LastName: r.LastName, Username: r.Username}
}

// targetFromChat makes a Target from what Telegram returned for a chat (private chats are users).
func (c *Ctx) targetFromChat(chat *gotgbot.ChatFullInfo, allowChats bool) (*Target, error) {
	if chat.Type == "private" {
		return &Target{ID: chat.Id, Name: strings.TrimSpace(chat.FirstName + " " + chat.LastName),
			Kind: userKind(false, chat.Username), FirstName: chat.FirstName, LastName: chat.LastName, Username: chat.Username}, nil
	}
	if !isChatKind(chat.Type) || !allowChats {
		return nil, &TargetError{Msg: c.T("targets.that_is_not_a_user")}
	}
	return &Target{ID: chat.Id, Name: chat.Title, Kind: chat.Type, Username: chat.Username, Title: chat.Title}, nil
}

func targetFromSenderChat(chat *gotgbot.Chat) *Target {
	return &Target{ID: chat.Id, Name: chat.Title, Kind: chat.Type, Username: chat.Username, Title: chat.Title}
}

// learnChat adds what Telegram told us about a user to the phone book.
func (c *Ctx) learnChat(chat *gotgbot.ChatFullInfo) {
	if chat.Type != "private" {
		return
	}
	u := &gotgbot.User{Id: chat.Id, FirstName: chat.FirstName, LastName: chat.LastName, Username: chat.Username}
	logErr("Could not store a user", c.App.Remember(c.Ctx, u))
}

// getChatByUsername asks Telegram about "@name": the typed API takes numeric ids only, so the parameter is overridden.
func (c *Ctx) getChatByUsername(username string) (*gotgbot.ChatFullInfo, error) {
	return c.Bot.GetChatWithContext(c.Ctx, 0, &gotgbot.GetChatOpts{
		RequestOpts: &gotgbot.RequestOpts{OverrideParams: map[string]any{"chat_id": "@" + username}},
	})
}

func (c *Ctx) targetByID(id int64, allowChats bool) (*Target, error) {
	if id < 0 {
		// Groups and channels have negative ids.
		if !allowChats {
			return nil, &TargetError{Msg: c.T("targets.that_is_not_a_valid_user")}
		}
		chat, err := c.Bot.GetChatWithContext(c.Ctx, id, nil)
		if err != nil {
			return nil, &TargetError{Msg: c.T("targets.i_don_t_know_that_chat")}
		}
		return c.targetFromChat(chat, allowChats)
	}
	row, err := c.App.UserByID(c.Ctx, id)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return targetFromRow(row), nil
	}
	chat, err := c.Bot.GetChatWithContext(c.Ctx, id, nil)
	if err != nil {
		return &Target{ID: id, Kind: "user"}, nil // never met: only the id is known
	}
	c.learnChat(chat)
	return c.targetFromChat(chat, allowChats)
}

func (c *Ctx) targetByUsername(username string, allowChats bool) (*Target, error) {
	row, err := c.App.UserByUsername(c.Ctx, username)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return targetFromRow(row), nil
	}
	chat, err := c.getChatByUsername(username)
	if err != nil {
		return nil, &TargetError{Msg: c.T("targets.i_don_t_know_yet_reply", i18nV{"username": username}), Unknown: true}
	}
	c.learnChat(chat)
	return c.targetFromChat(chat, allowChats)
}

// RepliedMessage is the message a command replies to, or nil. In forum topics every message "replies" to the
// service message that created the topic; that is not a person the command is aimed at.
func RepliedMessage(m *gotgbot.Message) *gotgbot.Message {
	if r := m.ReplyToMessage; r != nil && r.ForumTopicCreated == nil {
		return r
	}
	return nil
}

// firstArgumentOffset is where the first argument starts, in UTF-16 units (Telegram's entity offsets), or -1.
func firstArgumentOffset(text string) int {
	runes := []rune(text)
	i := 0
	for i < len(runes) && !unicode.IsSpace(runes[i]) {
		i++
	}
	if i == 0 || i == len(runes) {
		return -1
	}
	for i < len(runes) && unicode.IsSpace(runes[i]) {
		i++
	}
	if i == len(runes) {
		return -1
	}
	return len(utf16.Encode(runes[:i]))
}

// utf16Slice is part of a text by UTF-16 offsets; end < 0 means "to the end".
func utf16Slice(text string, start, end int) string {
	units := utf16.Encode([]rune(text))
	if start > len(units) {
		start = len(units)
	}
	if end < 0 || end > len(units) {
		end = len(units)
	}
	return string(utf16.Decode(units[start:end]))
}

// stripNameRemainder drops the rest of a person's name from the start of the extra data. Telegram often links
// only part of a name ("Jan" of "Jan Kowalski") and leaves the rest as plain text; without this, "Kowalski"
// would end up as the first word of the reason.
func stripNameRemainder(extra, linked, fullName string) string {
	if fullName == "" {
		return extra
	}
	nameWords, linkedWords := strings.Fields(fullName), strings.Fields(linked)
	if len(linkedWords) > len(nameWords) {
		return extra
	}
	for i, w := range linkedWords {
		if nameWords[i] != w {
			return extra // the link is not the start of the name; nothing to drop
		}
	}
	plain := func(w string) string { return strings.ToLower(strings.Trim(w, ",.:;")) }
	remaining := nameWords[len(linkedWords):]
	words := splitN(extra, len(remaining)+1)
	if len(remaining) > 0 && len(words) >= len(remaining) {
		same := true
		for i, w := range remaining {
			if plain(words[i]) != plain(w) {
				same = false
				break
			}
		}
		if same {
			if len(words) > len(remaining) {
				return strings.TrimSpace(words[len(remaining)])
			}
			return ""
		}
	}
	return extra
}

// splitN splits on whitespace into at most n parts; the last part keeps the rest of the text (like str.split(maxsplit=n-1)).
func splitN(s string, n int) []string {
	var parts []string
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	for len(parts) < n-1 && s != "" {
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			parts = append(parts, s)
			return parts
		}
		parts = append(parts, s[:i])
		s = strings.TrimLeftFunc(s[i:], unicode.IsSpace)
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

func looksLikeUser(token string) bool {
	return idRE.MatchString(token) || (strings.HasPrefix(token, "@") && usernameRE.MatchString(token))
}

// ResolveTarget returns the user a command refers to (plus extra data), or a *TargetError.
func (c *Ctx) ResolveTarget(allowBots, allowChats bool) (*Target, error) {
	msg := c.Msg
	text := msg.Text
	parts := splitN(c.Args, 2)
	reply := RepliedMessage(msg)

	check := func(t *Target) (*Target, error) {
		if t.Kind == "bot" && !allowBots {
			return nil, &TargetError{Msg: c.T("targets.bots_can_t_hold_roles")}
		}
		return t, nil
	}

	// 1. A mention standing in the first argument position.
	if start := firstArgumentOffset(text); start >= 0 {
		for _, e := range msg.Entities {
			if int(e.Offset) != start {
				continue
			}
			linked := utf16Slice(text, int(e.Offset), int(e.Offset+e.Length))
			extra := strings.Trim(utf16Slice(text, int(e.Offset+e.Length), -1), " \t\n,:")
			var target *Target
			var err error
			switch {
			case e.Type == "text_mention" && e.User != nil:
				if target, err = check(targetFromUser(e.User)); err == nil {
					extra = strings.Trim(stripNameRemainder(extra, linked, FullName(e.User)), " \t\n,:")
				}
			case e.Type == "mention":
				if target, err = c.targetByUsername(strings.TrimPrefix(linked, "@"), allowChats); err == nil {
					target, err = check(target)
				}
			default:
				continue
			}
			if err != nil {
				return nil, err
			}
			target.Extra = extra
			return target, nil
		}
	}

	// 2. An id or @username typed as the first argument. With a reply, a plain word is extra data for the
	//    replied-to person, not a user name.
	if len(parts) > 0 && (reply == nil || looksLikeUser(parts[0])) {
		first, extra := parts[0], ""
		if len(parts) > 1 {
			extra = strings.TrimSpace(parts[1])
		}
		var target *Target
		var err error
		if idRE.MatchString(first) {
			id, perr := strconv.ParseInt(first, 10, 64)
			if perr != nil || id == 0 {
				return nil, &TargetError{Msg: c.T("targets.that_is_not_a_valid_user")}
			}
			target, err = c.targetByID(id, allowChats)
		} else {
			match := usernameRE.FindStringSubmatch(first)
			if match == nil {
				return nil, &TargetError{Msg: c.T("targets.hint")}
			}
			target, err = c.targetByUsername(match[1], allowChats)
		}
		if err == nil {
			target, err = check(target)
		}
		if err != nil {
			return nil, err
		}
		target.Extra = extra
		return target, nil
	}

	// 3. The message being replied to; everything typed is extra data.
	if reply == nil {
		return nil, &TargetError{Msg: c.T("targets.hint")}
	}
	sender, senderChat := reply.From, reply.SenderChat
	if sender == nil && senderChat == nil {
		return nil, &TargetError{Msg: c.T("targets.i_can_t_tell_who_wrote")}
	}
	var target *Target
	switch {
	case sender == nil || placeholderSenders[sender.Id]:
		// Written on behalf of a channel or group (or by a Telegram service account).
		switch {
		case senderChat != nil && allowChats:
			target = targetFromSenderChat(senderChat)
			if !isChatKind(target.Kind) {
				return nil, &TargetError{Msg: c.T("targets.that_is_not_a_user")}
			}
		case !allowChats || sender == nil:
			return nil, &TargetError{Msg: c.T("common.that_message_was_not_sent_by")}
		default:
			target = targetFromUser(sender)
		}
	default:
		target = targetFromUser(sender)
	}
	target, err := check(target)
	if err != nil {
		return nil, err
	}
	target.Extra = strings.TrimSpace(c.Args)
	return target, nil
}

// SenderTarget is whoever wrote the message (sender_as_target for a reply to it). Nil when it was written on
// behalf of a chat or by one of the accounts Telegram uses for that.
func SenderTarget(m *gotgbot.Message) *Target {
	if m == nil || m.From == nil || placeholderSenders[m.From.Id] {
		return nil
	}
	return targetFromUser(m.From)
}

// SubjectOfSender is whoever sent the command (sender_as_target). A premium user or an anonymous admin can write on
// behalf of a channel or group; then the target is that chat, not the user.
func SubjectOfSender(m *gotgbot.Message) *Target {
	if m.SenderChat != nil && (m.From == nil || placeholderSenders[m.From.Id]) {
		return targetFromSenderChat(m.SenderChat)
	}
	if m.From == nil {
		return nil
	}
	return targetFromUser(m.From)
}
