package modules

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Saved messages anyone can ask for. Names are one word, case-insensitive.

const (
	maxNotes     = 300
	maxNoteName  = 64
	maxListed    = 300
	cbNotesClear = "notes:clear:" // notes:clear:yes / notes:clear:no
	noteCooldown = 3 * time.Second
)

var noteNameRE = regexp.MustCompile(`^[\p{L}\p{N}_.\-]{1,64}$`)

func registerNotes(r *core.Router) {
	r.Command(cmdNotes, "notes")
	r.Command(cmdGet, "get")
	r.Command(cmdSave, "save")
	r.Command(cmdClear, "clear")
	r.Command(cmdClearall, "clearall")
	r.Button(cbNotesClear, cbClearall)
	// After the modules that act on messages (flood, blacklists...), before keyword filters.
	r.Watch(40, "note hashtag", watchHashtag)
}

// checkNoteName is the stored form of a name typed by a person (a leading # is allowed); the string is a sentence for
// people when it is not acceptable.
func checkNoteName(c *core.Ctx, word string) (string, string) {
	name := strings.ToLower(strings.TrimLeft(strings.TrimSpace(word), "#"))
	if name == "" {
		return "", c.T("notestore.a_note_needs_a_name")
	}
	if !noteNameRE.MatchString(name) {
		return "", c.T("notestore.a_note_name_is_a_single", core.V{"MAX_NAME": maxNoteName})
	}
	return name, ""
}

// splitName is the name and the rest of the arguments, which may span several lines.
func splitName(args string) (string, string) {
	args = strings.TrimSpace(args)
	i := strings.IndexFunc(args, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' || r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029
	})
	if i < 0 {
		return args, ""
	}
	return args[:i], strings.TrimSpace(args[i:])
}

func noteNames(c *core.Ctx, chatID int64) ([]string, error) {
	rows, err := c.App.DB.Pool.Query(c.Ctx, `SELECT name FROM notes WHERE chat_id = $1 ORDER BY name`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func getNote(c *core.Ctx, chatID int64, name string) (*savedRow, error) {
	var content string
	var mt, mid *string
	found, err := c.App.DB.Found(c.Ctx, `SELECT content, media_type, media_file_id FROM notes WHERE chat_id = $1 AND name = $2`,
		[]any{chatID, name}, &content, &mt, &mid)
	if err != nil || !found {
		return nil, err
	}
	return &savedRow{content, deref(mt), deref(mid)}, nil
}

func cmdNotes(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	names, err := noteNames(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	chatTitle := c.Msg.Chat.Title
	if chatTitle == "" {
		chatTitle = c.T("common.this_chat")
	}
	title := c.T("notes.notes_in", core.V{"v": text.Esc(chatTitle)})
	if len(names) == 0 {
		c.Say(c.T("notes.there_are_none_yet_add_one", core.V{"title": title}))
		return nil
	}
	lines := []string{title, ""}
	for i, n := range names {
		if i >= maxListed {
			break
		}
		lines = append(lines, "• <code>"+text.Esc(n)+"</code>")
	}
	lines = append(lines, "", c.T("notes.get_one_with_get_lt_name"))
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdGet(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	word, _ := splitName(c.Args)
	if word == "" {
		c.Say(c.T("notes.use_get_lt_name_gt_notes"))
		return nil
	}
	name, problem := checkNoteName(c, word)
	if problem != "" {
		c.Say(text.Esc(problem))
		return nil
	}
	sent, err := sendNote(c, name)
	if err != nil {
		return err
	}
	if !sent {
		c.Say(c.T("notes.there_is_no_note_called_notes", core.V{"name": text.Esc(name)}))
	}
	return nil
}

func cmdSave(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	word, body := splitName(core.ArgsOf(c.Msg, c.Args))
	usage := c.T("notes.use_save_lt_name_gt_lt")
	if word == "" {
		c.Say(usage)
		return nil
	}
	name, problem := checkNoteName(c, word)
	if problem != "" {
		c.Say(text.Esc(problem))
		return nil
	}
	replied := core.RepliedMessage(c.Msg)
	media := core.MediaOf(c.Msg)
	if media == nil {
		media = core.MediaOf(replied)
	}
	if body == "" && replied != nil {
		body = core.TextOf(replied)
	}
	if body == "" && media == nil {
		c.Say(c.T("notes.tell_me_what_to_save_write", core.V{"usage": usage}))
		return nil
	}
	if err := c.Validate(body, media != nil); err != nil {
		return sayMarkupError(c, err)
	}

	chatID := c.Msg.Chat.Id
	exists, err := c.App.DB.Found(c.Ctx, `SELECT 1 FROM notes WHERE chat_id = $1 AND name = $2`, []any{chatID, name}, new(int))
	if err != nil {
		return err
	}
	if !exists {
		var total int64
		if err := c.App.DB.Row(c.Ctx, `SELECT count(*) FROM notes WHERE chat_id = $1`, []any{chatID}, &total); err != nil {
			return err
		}
		if total >= maxNotes {
			c.Say(c.T("notes.this_chat_is_full_notes_remove", core.V{"MAX_NOTES": maxNotes}))
			return nil
		}
	}
	var mt, mid any
	if media != nil {
		mt, mid = media.Kind, media.FileID
	}
	if _, err := c.App.DB.Exec(c.Ctx, `INSERT INTO notes (chat_id, name, content, media_type, media_file_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (chat_id, name) DO UPDATE SET content = EXCLUDED.content,
media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id`, chatID, name, body, mt, mid, c.User.Id); err != nil {
		return err
	}
	verb := c.T("common.updated")
	if !exists {
		verb = c.T("notes.saved")
	}
	c.Say(c.T("notes.the_note_get_it_with_get", core.V{"verb": verb, "name": text.Esc(name)}))
	return nil
}

func cmdClear(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	word, _ := splitName(c.Args)
	if word == "" {
		c.Say(c.T("notes.use_clear_lt_name_gt"))
		return nil
	}
	name, problem := checkNoteName(c, word)
	if problem != "" {
		c.Say(text.Esc(problem))
		return nil
	}
	n, err := c.App.DB.Exec(c.Ctx, `DELETE FROM notes WHERE chat_id = $1 AND name = $2`, c.Msg.Chat.Id, name)
	if err != nil {
		return err
	}
	if n > 0 {
		c.Say(c.T("notes.removed_the_note", core.V{"name": text.Esc(name)}))
	} else {
		c.Say(c.T("notes.there_is_no_note_called", core.V{"name": text.Esc(name)}))
	}
	return nil
}

func cmdClearall(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	names, err := noteNames(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		c.Say(c.T("notes.there_are_no_notes_in_this"))
		return nil
	}
	_, err = c.Reply(fmt.Sprintf("Delete all <b>%d</b> notes of this chat? This can't be undone.", len(names)), core.Keyboard(
		[]gotgbot.InlineKeyboardButton{core.Button("Delete all", cbNotesClear+"yes"), core.Button(c.T("common.cancel"), cbNotesClear+"no")}))
	return err
}

func cbClearall(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.MemberStatus(c.Msg.Chat.Id, c.User.Id) != "creator" {
		c.Answer(c.T("common.only_the_owner_of_this_chat"), true)
		return nil
	}
	if c.Cb.Data == cbNotesClear+"yes" {
		removed, err := c.App.DB.Exec(c.Ctx, `DELETE FROM notes WHERE chat_id = $1`, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Answer("", false)
		return c.Edit(c.T("notes.done_notes_deleted", core.V{"removed": removed}), nil)
	}
	c.Answer("", false)
	return c.Edit(c.T("notes.cancelled_the_notes_are_unchanged"), nil)
}

// sendNote sends a note in answer to the current message. False when the chat has no such note.
func sendNote(c *core.Ctx, name string) (bool, error) {
	row, err := getNote(c, c.Msg.Chat.Id, name)
	if err != nil || row == nil {
		return false, err
	}
	c.SendSaved(c.User, row.Content, row.MediaType, row.MediaID, core.SendOpts{Label: c.T("notes.note", core.V{"name": name})})
	return true, nil
}

// watchHashtag answers a message that starts with #name, where name is a note of this chat.
func watchHashtag(c *core.Ctx) (bool, error) {
	body := c.Msg.Text
	if body == "" {
		body = c.Msg.Caption
	}
	if c.Edited || body == "" || c.User == nil || c.User.IsBot || !isGroup(c.Msg.Chat.Type) || !strings.HasPrefix(body, "#") {
		return false, nil
	}
	first := strings.Fields(body)
	if len(first) == 0 {
		return false, nil
	}
	name, problem := checkNoteName(c, first[0])
	if problem != "" {
		return false, nil
	}
	row, err := getNote(c, c.Msg.Chat.Id, name)
	if err != nil || row == nil {
		return false, err
	}
	if !c.App.Cooldown(fmt.Sprintf("note:%d/%s", c.Msg.Chat.Id, name), noteCooldown) {
		return false, nil
	}
	c.SendSaved(c.User, row.Content, row.MediaType, row.MediaID, core.SendOpts{Label: c.T("notes.note", core.V{"name": name})})
	return true, nil
}
