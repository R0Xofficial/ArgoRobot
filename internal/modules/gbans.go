package modules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"
)

// Global bans. The commands of the bot's staff answer in English, like everything they do.

const (
	maxGbanReason  = 500
	cbGban         = "gban:" // gban:apr:<request id> / gban:dec:<request id>
	importChunk    = 5000
	exportCooldown = 30 * time.Second
	recentWindow   = 30 * time.Second
)

func registerGbans(r *core.Router) {
	r.WatchMembers(-100, "gbans:member", watchGbanMember)
	r.WatchReactions(-100, "gbans:reaction", watchGbanReaction)
	r.Watch(-100, "gbans", watchGbans)
	r.CommandRole(core.Support, cmdGban, "gban", "dgban")
	r.CommandRole(core.Support, cmdUngban, "ungban")
	r.Command(cmdGstat, "gstat", "gbanstat")
	r.Command(cmdGbans, "gbans", "enforcegban")
	r.CommandRole(core.Owner, cmdGimport, "gimport")
	r.CommandRole(core.Owner, cmdGexport, "gexport")
	r.Button(cbGban, cbGbanRequest)
	core.RegisterInfo("gbans", false, func(c *core.Ctx, userID, _ int64) []core.InfoFlag {
		banned, _ := c.App.IsGbanned(c.Ctx, userID)
		return []core.InfoFlag{{Label: c.T("gbans.globally_banned"), Value: banned}}
	})
}

// --- Wording ----------------------------------------------------------------------------

func threadOf(m *gotgbot.Message) int64 {
	if m.IsTopicMessage {
		return m.MessageThreadId
	}
	return 0
}

func atName(s string) string {
	s = strings.TrimLeft(strings.TrimSpace(s), "@")
	if s == "" {
		return ""
	}
	return "@" + s
}

type place struct{ chat, msg, thread int64 }

func replyPlace(m *gotgbot.Message) place {
	if m.Chat.Type == "private" {
		return place{chat: m.Chat.Id}
	}
	return place{m.Chat.Id, m.MessageId, threadOf(m)}
}

// originHTML is the "Initiated From" line: where the command was given, with a link only for public chats.
func originHTML(l i18n.Lang, m *gotgbot.Message) string {
	chat := m.Chat
	var where string
	switch {
	case chat.Type == "private":
		first := ""
		if m.From != nil {
			first = m.From.FirstName
		}
		where = l.T("gbans.pm_with", i18n.V{"first_name": text.Esc(first)})
	case chat.Username != "":
		where = fmt.Sprintf(`<a href="https://t.me/%s/%d">%s</a>`, text.Esc(chat.Username), m.MessageId, text.Esc(chat.Title))
	default:
		where = text.Esc(chat.Title)
	}
	return fmt.Sprintf("%s [<code>%d</code>]", where, chat.Id)
}

type logParts struct {
	tag, origin, target, reason, admin string
	oldReason, approvedBy              string
	noTimestamp                        bool
}

func gbanLogText(l i18n.Lang, p logParts) string {
	lines := []string{
		"<b>" + p.tag + "</b>",
		l.T("gbans.initiated_from", i18n.V{"origin": p.origin}),
		"",
		l.T("gbans.user", i18n.V{"target": p.target}),
		l.T("gbans.reason", i18n.V{"reason": text.Esc(p.reason)}),
	}
	if p.oldReason != "" {
		lines = append(lines, "<b>Old Reason:</b> <code>"+text.Esc(p.oldReason)+"</code>")
	}
	if !p.noTimestamp {
		lines = append(lines, "<b>Timestamp:</b> "+core.Timestamp(time.Time{}))
	}
	lines = append(lines, "<b>Admin:</b> "+p.admin)
	if p.approvedBy != "" {
		lines = append(lines, "<b>Approved by:</b> "+p.approvedBy)
	}
	return strings.Join(lines, "\n")
}

func gbanTag(old *core.Gban) string {
	if old != nil {
		return "#GBANUPDATE"
	}
	return "#GBANNED"
}

func gbanDone(l i18n.Lang, old *core.Gban) string {
	if old != nil {
		return l.T("gbans.done_gban_reason_updated_old_reason", i18n.V{"reason": text.Esc(old.Reason)})
	}
	return l.T("gbans.done_gbanned")
}

// notifyChat says something in the chat a staff command came from, in its topic, never failing.
func notifyChat(ctx context.Context, bot *gotgbot.Bot, p place, msg string) {
	opts := &gotgbot.SendMessageOpts{ParseMode: "HTML", MessageThreadId: p.thread,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}}
	if p.msg != 0 {
		opts.ReplyParameters = &gotgbot.ReplyParameters{MessageId: p.msg, AllowSendingWithoutReply: true}
	}
	if _, err := bot.SendMessageWithContext(ctx, p.chat, msg, opts); err != nil {
		slog.Debug("Could not answer", "chat", p.chat, "err", err)
	}
}

// --- Who may be globally banned ---------------------------------------------------------

// gbanProtection says why this user can never be globally banned, or "".
func gbanProtection(c *core.Ctx, userID int64) string {
	switch {
	case core.IsImmune(userID):
		return c.T("common.i_can_t_take_action_against")
	case userID == c.Bot.Id || c.App.GetRole(c.Ctx, userID) != core.NoRole:
		return c.T("gbans.privileged_message")
	}
	if ok, _ := c.App.IsWhitelisted(c.Ctx, userID); ok {
		return c.T("gbans.that_user_is_whitelisted_and_can")
	}
	return ""
}

// --- Removing people --------------------------------------------------------------------

var recent = struct {
	sync.Mutex
	m map[[2]int64]time.Time
}{m: map[[2]int64]time.Time{}}

// firstRecently is true the first time a user is dealt with in a chat within a short while: updates arrive in
// parallel, so a banned user who sends five messages at once would be banned, and announced, five times.
func firstRecently(chatID, userID int64) bool {
	recent.Lock()
	defer recent.Unlock()
	now := time.Now()
	key := [2]int64{chatID, userID}
	if last, ok := recent.m[key]; ok && now.Sub(last) < recentWindow {
		return false
	}
	if len(recent.m) > 10_000 {
		for k, t := range recent.m {
			if now.Sub(t) >= recentWindow {
				delete(recent.m, k)
			}
		}
	}
	recent.m[key] = now
	return true
}

func forgetRecent(chatID, userID int64) {
	recent.Lock()
	delete(recent.m, [2]int64{chatID, userID})
	recent.Unlock()
}

func retryAfter(err error) time.Duration {
	if tg := core.TgError(err); tg != nil && tg.Code == 429 && tg.ResponseParams != nil {
		return time.Duration(tg.ResponseParams.RetryAfter) * time.Second
	}
	return 0
}

// sleepFor waits when Telegram asks to slow down (tests replace it).
var sleepFor = time.Sleep

// gbanIn bans in one chat. False when Telegram refuses (usually: the bot is not an admin there).
func gbanIn(ctx context.Context, c *core.Ctx, chatID, userID int64) bool {
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := c.Bot.BanChatMemberWithContext(ctx, chatID, userID, nil)
		if err == nil {
			_ = c.App.RecordGbanBan(ctx, userID, chatID)
			return true
		}
		if wait := retryAfter(err); wait > 0 && attempt == 1 {
			sleepFor(wait)
			continue
		}
		slog.Warn("Could not ban", "user", userID, "chat", chatID, "err", err)
		return false
	}
	return false
}

func removeFrom(c *core.Ctx, chatID, userID int64) bool {
	return c.GbanRemovable(chatID, userID) && gbanIn(c.Ctx, c, chatID, userID)
}

func gbanAlert(c *core.Ctx, chatID int64, ban *core.Gban, thread int64, name string) {
	if name == "" {
		if row, err := c.App.UserByID(c.Ctx, ban.UserID); err == nil && row != nil {
			name = row.Name
		}
	}
	if name == "" {
		name = ban.Name
	}
	lines := []string{
		c.T("gbans.alert_detected_globally_banned_user"),
		c.T("gbans.i_banned_them_here"),
		c.T("gbans.user_2", core.V{"user_id": text.UserRef(ban.UserID, name)}),
		c.T("gbans.reason", core.V{"reason": text.Esc(ban.Reason)}),
	}
	if support := atName(c.App.Cfg.SupportChatUsername); support != "" {
		lines = append(lines, "<b>Appeal Chat:</b> "+text.Esc(support))
	}
	notifyChat(c.Ctx, c.Bot, place{chat: chatID, thread: thread}, strings.Join(lines, "\n"))
}

// enforceGban removes them and says so. False when that failed (the bot is probably not allowed to ban here).
func enforceGban(c *core.Ctx, chatID, userID, thread int64, name string) bool {
	ban, err := c.App.GetGban(c.Ctx, userID)
	if err != nil || ban == nil {
		return false
	}
	if !firstRecently(chatID, userID) {
		return true // another update of theirs is dealing with it
	}
	if !gbanIn(c.Ctx, c, chatID, userID) {
		forgetRecent(chatID, userID) // try again with their next message
		return false
	}
	gbanAlert(c, chatID, ban, thread, name)
	return true
}

// gbanTasks counts the lifting of bans that runs in the background.
var gbanTasks sync.WaitGroup

// unbanPause is the pause between two chats while a ban is lifted.
var unbanPause = 50 * time.Millisecond

func spawnUnban(c *core.Ctx, userID int64, origin *place) {
	gbanTasks.Add(1)
	go func() {
		defer gbanTasks.Done()
		propagateUnban(c, userID, origin)
	}()
}

// propagateUnban unbans them everywhere the bot knows of, then reports to the chat the command came from.
func propagateUnban(c *core.Ctx, userID int64, origin *place) {
	ctx := context.Background()
	started := time.Now()
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Could not lift the bans", "user", userID, "panic", r)
			}
		}()
		var targets []int64
		seen := map[int64]bool{}
		add := func(id int64) {
			if !seen[id] {
				seen[id] = true
				targets = append(targets, id)
			}
		}
		banned, err := c.App.GbanBannedChats(ctx, userID)
		if err != nil {
			slog.Error("Could not lift the bans", "user", userID, "err", err)
			return
		}
		for _, id := range banned {
			add(id)
		}
		chats, err := c.App.ChatsOfUser(ctx, userID)
		if err != nil {
			slog.Error("Could not lift the bans", "user", userID, "err", err)
			return
		}
		for _, ch := range chats {
			if (ch.Type == "group" || ch.Type == "supergroup") && (ch.BotAdmin == nil || *ch.BotAdmin) {
				add(ch.ID)
			}
		}
		if origin != nil && origin.chat < 0 {
			add(origin.chat) // the chat the command came from, always
		}
		for _, id := range targets {
			_, err := c.Bot.UnbanChatMemberWithContext(ctx, id, userID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
			if wait := retryAfter(err); wait > 0 {
				sleepFor(wait)
			} else if err != nil {
				slog.Debug("Could not unban", "user", userID, "chat", id, "err", err)
			}
			time.Sleep(unbanPause)
		}
		if err := c.App.ForgetGbanBans(ctx, userID); err != nil {
			slog.Error("Could not forget the bans", "err", err)
		}
	}()
	if origin != nil {
		seconds := strconv.FormatFloat(float64(time.Since(started).Round(10*time.Millisecond))/float64(time.Second), 'f', -1, 64)
		notifyChat(ctx, c.Bot, *origin, i18n.English.T("gbans.user_has_been_un_gbanned_time", i18n.V{"seconds": seconds}))
	}
}

// --- Applying a ban ---------------------------------------------------------------------

type banJob struct {
	targetID    int64
	targetName  string
	reason      string
	adminID     int64
	adminName   string
	approvedBy  int64
	approvedNm  string
	originChat  int64
	originGroup bool
	deleteChat  int64
	deleteMsg   int64
}

// applyGban records the ban, removes the user where the command was given and returns the previous ban.
func applyGban(c *core.Ctx, j banJob) (*core.Gban, error) {
	old, err := c.App.AddGban(c.Ctx, j.targetID, j.targetName, j.reason, j.adminID, j.adminName, j.approvedBy, j.approvedNm)
	if err != nil {
		return nil, err
	}
	if j.deleteMsg != 0 {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, j.deleteChat, j.deleteMsg, nil)
	}
	if j.originGroup {
		// Only when they are in the chat: a ban in a chat they never joined would only add a row to its ban list.
		if m := c.GetMember(j.originChat, j.targetID); m != nil && memberPresent(*m) {
			removeFrom(c, j.originChat, j.targetID)
		}
	}
	return old, nil
}

func requestKeyboard(id int64) *gotgbot.InlineKeyboardMarkup {
	l := i18n.English
	return core.Keyboard([]gotgbot.InlineKeyboardButton{
		core.Button(l.T("gbans.approve"), cbGban+"apr:"+strconv.FormatInt(id, 10)),
		core.Button(l.T("common.decline"), cbGban+"dec:"+strconv.FormatInt(id, 10)),
	})
}

// --- /gban, /dgban, /ungban -------------------------------------------------------------

func askSudo(c *core.Ctx, kind, tag string, target *core.Target, reason string, old *core.Gban, targetMsg int64) {
	l := i18n.English
	actor := c.User
	origin := originHTML(l, c.Msg)
	id, err := c.App.CreateGbanRequest(c.Ctx, core.GbanRequest{
		TargetID: target.ID, TargetName: target.Name, Kind: kind, Reason: reason,
		RequestedBy: actor.Id, RequestedByName: core.FullName(actor),
		ChatID: c.Msg.Chat.Id, MsgID: c.Msg.MessageId, ThreadID: threadOf(c.Msg), TargetMsgID: targetMsg, OriginHTML: origin,
	})
	if err != nil {
		c.App.ReportError("/"+c.Cmd, err)
		return
	}
	if id == 0 {
		c.Say(l.T("gbans.there_is_already_a_pending_request", core.V{"user_id": text.UserRef(target.ID, target.Name)}))
		return
	}
	parts := logParts{tag: tag, origin: origin, target: text.UserRef(target.ID, target.Name), reason: reason,
		admin: text.UserRef(actor.Id, core.FullName(actor)), noTimestamp: true}
	if old != nil {
		parts.oldReason = old.Reason
	}
	body, markup := gbanLogText(l, parts), requestKeyboard(id)
	var delivered []core.GbanDelivery
	for _, approver := range c.App.GbanApprovers(c.Ctx) { // every Sudo user and above gets it in their private chat
		if msgID, ok := c.App.SendTo(c.Ctx, approver, body, markup); ok {
			delivered = append(delivered, core.GbanDelivery{Chat: approver, Msg: msgID})
		}
	}
	if len(delivered) > 0 {
		if err := c.App.SaveGbanDeliveries(c.Ctx, id, delivered); err != nil {
			c.App.ReportError("/"+c.Cmd, err)
		}
	} else if !c.App.RoleLog(c.Ctx, body, markup) { // nobody reachable: the log chat still gets it
		_ = c.App.DropGbanRequest(c.Ctx, id)
		c.Say(l.T("gbans.i_couldn_t_deliver_your_request"))
		return
	}
	c.Say(l.T("gbans.request_sent"))
}

// tellBanned lets somebody know, in their private chat, that they have been globally banned. They may never have
// started the bot, in which case nothing happens.
func tellBanned(c *core.Ctx, userID int64, reason string, updated bool, admin, approvedBy string) {
	head := "You have been globally banned."
	if updated {
		head = "The reason of your global ban has been updated."
	}
	msg := notice(head, reason, admin, approvedBy)
	if support := atName(c.App.Cfg.SupportChatUsername); support != "" {
		msg += "\nIf you think this is a mistake, contact " + text.Esc(support) + "."
	}
	c.App.SendTo(c.Ctx, userID, msg, nil)
}

func tellUnbanned(c *core.Ctx, userID int64, reason, admin, approvedBy string) {
	c.App.SendTo(c.Ctx, userID, notice("Your global ban has been lifted.", reason, admin, approvedBy), nil)
}

// notice is what the person is told: what happened, why, when and by whom (and who approved it).
func notice(head, reason, admin, approvedBy string) string {
	msg := "<b>" + head + "</b>\n<b>Reason:</b> <code>" + text.Esc(reason) + "</code>\n<b>Timestamp:</b> " +
		core.Timestamp(time.Time{}) + "\n<b>Admin:</b> " + admin
	if approvedBy != "" {
		msg += "\n<b>Approved by:</b> " + approvedBy
	}
	return msg
}

func targetError(c *core.Ctx, err error) bool {
	var te *core.TargetError
	if errors.As(err, &te) {
		c.Say(text.Esc(te.Msg))
	} else {
		c.App.ReportError("/"+c.Cmd, err)
	}
	return true
}

func cmdGban(c *core.Ctx) error {
	c.L = i18n.English
	deleting := c.Cmd == "dgban"
	replied := core.RepliedMessage(c.Msg)
	var target *core.Target
	if deleting {
		if replied == nil {
			c.Say(c.T("common.reply_to_the_message_of_the"))
			return nil
		}
		if target = core.SenderTarget(replied); target == nil {
			c.Say(c.T("common.that_message_was_not_sent_by"))
			return nil
		}
		target.Extra = strings.TrimSpace(c.Args)
	} else {
		var err error
		if target, err = c.ResolveTarget(true, false); err != nil {
			targetError(c, err)
			return nil
		}
	}
	who := text.UserRef(target.ID, target.Name)
	reason := strings.TrimSpace(target.Extra)
	if core.IsImmune(target.ID) {
		c.Say(c.T("common.i_can_t_take_action_against"))
		return nil
	}
	if reason == "" {
		c.Say(c.T("gbans.no_reason"))
		return nil
	}
	if len([]rune(reason)) > maxGbanReason {
		c.Say(c.T("gbans.the_reason_is_too_long_at", core.V{"MAX_REASON_LENGTH": maxGbanReason}))
		return nil
	}
	if p := gbanProtection(c, target.ID); p != "" {
		c.Say(p)
		return nil
	}
	if pending, err := c.App.HasPendingGban(c.Ctx, target.ID); err != nil {
		return err
	} else if pending {
		c.Say(c.T("gbans.there_is_already_a_pending_request_2", core.V{"who": who}))
		return nil
	}
	old, err := c.App.GetGban(c.Ctx, target.ID)
	if err != nil {
		return err
	}
	if old != nil && strings.TrimSpace(old.Reason) == reason {
		c.Say(c.T("gbans.is_already_globally_banned_for_the", core.V{"who": who}))
		return nil
	}
	var targetMsg int64
	if deleting {
		targetMsg = replied.MessageId
	}
	if c.Role < core.Sudo {
		kind, tag := "gban", "#GBANREQUEST"
		if deleting {
			kind = "dgban"
		}
		if old != nil {
			tag = "#GBANUPDATEREQUEST"
		}
		askSudo(c, kind, tag, target, reason, old, targetMsg)
		return nil
	}
	admin := c.User
	job := banJob{targetID: target.ID, targetName: target.Name, reason: reason, adminID: admin.Id, adminName: core.FullName(admin),
		originChat: c.Msg.Chat.Id, originGroup: isGroup(c.Msg.Chat.Type)}
	if deleting {
		job.deleteChat, job.deleteMsg = c.Msg.Chat.Id, targetMsg
	}
	old, err = applyGban(c, job)
	if err != nil {
		return err
	}
	parts := logParts{tag: gbanTag(old), origin: originHTML(c.L, c.Msg), target: who, reason: reason,
		admin: text.UserRef(admin.Id, core.FullName(admin))}
	if old != nil {
		parts.oldReason = old.Reason
	}
	c.App.RoleLog(c.Ctx, gbanLogText(c.L, parts), nil)
	tellBanned(c, target.ID, reason, old != nil, text.UserRef(admin.Id, core.FullName(admin)), "")
	c.Say(gbanDone(c.L, old))
	return nil
}

func cmdUngban(c *core.Ctx) error {
	c.L = i18n.English
	target, err := c.ResolveTarget(true, false)
	if err != nil {
		targetError(c, err)
		return nil
	}
	who := text.UserRef(target.ID, target.Name)
	reason := strings.TrimSpace(target.Extra)
	switch {
	case core.IsImmune(target.ID):
		c.Say(c.T("common.i_can_t_take_action_against"))
		return nil
	case reason == "":
		c.Say(c.T("gbans.no_reason"))
		return nil
	case len([]rune(reason)) > maxGbanReason:
		c.Say(c.T("gbans.the_reason_is_too_long_at", core.V{"MAX_REASON_LENGTH": maxGbanReason}))
		return nil
	case target.ID == c.Bot.Id || c.App.GetRole(c.Ctx, target.ID) != core.NoRole:
		c.Say(c.T("gbans.privileged_users_are_never_gbanned"))
		return nil
	}
	if banned, err := c.App.IsGbanned(c.Ctx, target.ID); err != nil {
		return err
	} else if !banned {
		c.Say(c.T("gbans.is_not_globally_banned", core.V{"who": who}))
		return nil
	}
	if pending, err := c.App.HasPendingGban(c.Ctx, target.ID); err != nil {
		return err
	} else if pending {
		c.Say(c.T("gbans.there_is_already_a_pending_request_2", core.V{"who": who}))
		return nil
	}
	if c.Role < core.Sudo {
		askSudo(c, "ungban", "#UNGBANREQUEST", target, reason, nil, 0)
		return nil
	}
	admin := c.User
	if removed, err := c.App.RemoveGban(c.Ctx, target.ID); err != nil {
		return err
	} else if !removed {
		c.Say(c.T("gbans.is_not_globally_banned", core.V{"who": who}))
		return nil
	}
	c.App.RoleLog(c.Ctx, gbanLogText(c.L, logParts{tag: "#UNGBANNED", origin: originHTML(c.L, c.Msg), target: who, reason: reason,
		admin: text.UserRef(admin.Id, core.FullName(admin))}), nil)
	tellUnbanned(c, target.ID, reason, text.UserRef(admin.Id, core.FullName(admin)), "")
	c.Say(c.T("gbans.let_s_give_them_another_chance"))
	p := replyPlace(c.Msg)
	spawnUnban(c, target.ID, &p)
	return nil
}

// --- The buttons under a request ----------------------------------------------------------

func cbGbanRequest(c *core.Ctx) error {
	c.L = i18n.English
	if c.Role < core.Sudo {
		c.Answer(c.T("gbans.only_sudo_users_and_above_can"), true)
		return nil
	}
	parts := strings.Split(c.Cb.Data, ":")
	if len(parts) != 3 || (parts[1] != "apr" && parts[1] != "dec") || !isDigits(parts[2]) {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	approving := parts[1] == "apr"
	id, _ := strconv.ParseInt(parts[2], 10, 64)
	request, err := c.App.ClaimGbanRequest(c.Ctx, id)
	if err != nil {
		return err
	}
	if request == nil {
		c.Answer(c.T("gbans.request_not_found_likely_already_processed"), true)
		c.RemoveButtons()
		return nil
	}
	approver := c.User
	approverRef := text.UserRef(approver.Id, core.FullName(approver))
	where := place{chat: request.ChatID}
	if request.ChatID < 0 {
		where.msg = request.MsgID
	}
	where.thread = request.ThreadID
	who := text.UserRef(request.TargetID, request.TargetName)
	requester := text.UserRef(request.RequestedBy, request.RequestedByName)
	shown := ""
	if c.Msg != nil {
		shown = c.Msg.OriginalHTML()
	}
	// finish shows the outcome in every copy of the request and, unless it is a cancellation, in the log chat
	// (when the request was itself a log message, that is the one being edited).
	finish := func(msg string, logIt bool) {
		edit := func(chat, id int64) {
			_, _, err := c.Bot.EditMessageTextWithContext(c.Ctx, &gotgbot.EditMessageTextOpts{
				ChatId: chat, MessageId: id, Text: msg, ParseMode: "HTML",
				LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
			})
			if err != nil {
				slog.Debug("Could not edit the request", "err", err)
			}
		}
		if c.Msg != nil {
			edit(c.Msg.Chat.Id, c.Msg.MessageId)
		}
		for _, d := range request.Deliveries {
			if c.Msg == nil || d.Chat != c.Msg.Chat.Id || d.Msg != c.Msg.MessageId {
				edit(d.Chat, d.Msg)
			}
		}
		if len(request.Deliveries) > 0 && logIt {
			var kept []string // the log doesn't need to say who pressed the button
			for _, line := range strings.Split(msg, "\n") {
				if !strings.HasPrefix(line, "<b>Approved by:</b>") {
					kept = append(kept, line)
				}
			}
			c.App.RoleLog(c.Ctx, strings.Join(kept, "\n"), nil)
		}
	}
	if !approving {
		c.Answer(c.T("gbans.request_declined"), false)
		finish(c.T("gbans.declined_by", core.V{"html_text": shown, "id": approverRef}), false) // nothing happened: nothing to log
		notifyChat(c.Ctx, c.Bot, where, c.T("gbans.request_declined_2"))
		return nil
	}
	if request.Kind == "gban" || request.Kind == "dgban" {
		if problem := gbanProtection(c, request.TargetID); problem != "" {
			c.Answer(c.T("gbans.this_user_can_no_longer_be"), true)
			finish(c.T("gbans.cancelled", core.V{"opt": shown, "problem": problem}), false)
			notifyChat(c.Ctx, c.Bot, where, c.T("gbans.request_cancelled", core.V{"problem": problem}))
			return nil
		}
		c.Answer(c.T("gbans.approved"), false)
		job := banJob{targetID: request.TargetID, targetName: request.TargetName, reason: request.Reason,
			adminID: request.RequestedBy, adminName: request.RequestedByName, approvedBy: approver.Id,
			approvedNm: core.FullName(approver), originChat: request.ChatID, originGroup: request.ChatID < 0}
		if request.Kind == "dgban" && request.TargetMsgID != 0 {
			job.deleteChat, job.deleteMsg = request.ChatID, request.TargetMsgID
		}
		old, err := applyGban(c, job)
		if err != nil {
			return err
		}
		parts := logParts{tag: gbanTag(old), origin: request.OriginHTML, target: who, reason: request.Reason, admin: requester,
			approvedBy: approverRef}
		if old != nil {
			parts.oldReason = old.Reason
		}
		finish(gbanLogText(c.L, parts), true)
		notifyChat(c.Ctx, c.Bot, where, gbanDone(c.L, old))
		tellBanned(c, request.TargetID, request.Reason, old != nil, requester, approverRef)
		return nil
	}
	// ungban
	removed, err := c.App.RemoveGban(c.Ctx, request.TargetID)
	if err != nil {
		return err
	}
	if !removed {
		c.Answer(c.T("gbans.this_user_is_not_globally_banned"), true)
		finish(c.T("gbans.cancelled_not_globally_banned_any_more", core.V{"opt": shown}), false)
		return nil
	}
	c.Answer(c.T("gbans.approved"), false)
	finish(gbanLogText(c.L, logParts{tag: "#UNGBANNED", origin: request.OriginHTML, target: who, reason: request.Reason,
		admin: requester, approvedBy: approverRef}), true)
	tellUnbanned(c, request.TargetID, request.Reason, requester, approverRef)
	origin := place{chat: request.ChatID, thread: request.ThreadID}
	if request.ChatID < 0 {
		origin.msg = request.MsgID
	}
	spawnUnban(c, request.TargetID, &origin)
	return nil
}

// --- /gstat -------------------------------------------------------------------------------

func cmdGstat(c *core.Ctx) error {
	viewer := c.User
	if viewer == nil || core.IsImmune(viewer.Id) {
		c.Say(c.T("gbans.i_can_t_tell_who_you"))
		return nil
	}
	targetID, stored := viewer.Id, core.FullName(viewer)
	if strings.TrimSpace(c.Args) != "" || core.RepliedMessage(c.Msg) != nil { // anyone may look somebody up
		target, err := c.ResolveTarget(true, false)
		if err != nil {
			targetError(c, err)
			return nil
		}
		targetID, stored = target.ID, target.Name
	}
	if core.IsImmune(targetID) {
		c.Say(c.T("gbans.this_account_is_immune_to_all"))
		return nil
	}
	ban, err := c.App.GetGban(c.Ctx, targetID)
	if err != nil {
		return err
	}
	if ban != nil && ban.Name != "" {
		stored = ban.Name
	}
	name := c.DisplayName(targetID, stored)
	title := c.T("gbans.global_ban_status")
	if targetID == viewer.Id {
		title = c.T("gbans.your_global_ban_status")
	}
	lines := []string{"<b>" + title + "</b>", c.T("gbans.user_3", core.V{"target_id": text.UserRef(targetID, name)}), ""}
	if role := c.App.GetRole(c.Ctx, targetID); role != core.NoRole {
		lines = append(lines, "This user is a member of my staff and can't have a global ban.")
	} else if ban != nil {
		lines = append(lines, "• <b>Status:</b> <code>Banned</code>",
			"• <b>Reason:</b> <code>"+text.Esc(ban.Reason)+"</code>",
			"• <b>Timestamp:</b> "+core.Timestamp(ban.CreatedAt))
		if ok, _ := c.App.IsWhitelisted(c.Ctx, targetID); ok {
			lines = append(lines, "• <b>Whitelisted:</b> <code>Yes</code> (not removed from chats)")
		}
		if c.Role >= core.Sudo {
			if ban.AdminID == 0 {
				lines = append(lines, "• <b>Admin:</b> This ban is imported.")
			} else {
				lines = append(lines, "• <b>Admin:</b> "+text.UserRef(ban.AdminID, ban.AdminName))
			}
		}
		if support := atName(c.App.Cfg.SupportChatUsername); support != "" {
			lines = append(lines, "• <b>Support:</b> "+text.Esc(support))
		}
	} else {
		lines = append(lines, "• <b>Status:</b> <code>Not Banned</code>")
	}
	return replyNoPreview(c, strings.Join(lines, "\n"))
}

// --- /gbans: enforcement in a chat --------------------------------------------------------

// cmdGbans: whether this chat removes globally banned users. On by default. Chat admins can look at the setting,
// only the chat's owner can change it.
func cmdGbans(c *core.Ctx) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg == "" {
		if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
			return nil
		}
		state := c.T("gbans.disabled")
		if c.App.Setting(c.Ctx, "gban_enforce", c.Msg.Chat.Id) {
			state = c.T("gbans.enabled")
		}
		c.Say(c.T("gbans.global_ban_enforcement_current_status_for", core.V{"opt": state}))
		return nil
	}
	on, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("gbans.invalid_choice_use_gbans_on_or"))
		return nil
	}
	if !c.RequireChatOwner() {
		return nil
	}
	if err := c.App.SetSetting(c.Ctx, "gban_enforce", c.Msg.Chat.Id, on); err != nil {
		return err
	}
	if on {
		c.Say(c.T("gbans.global_ban_enforcement_has_been_enabled"))
	} else {
		c.Say(c.T("gbans.global_ban_enforcement_has_been_disabled"))
	}
	return nil
}

// --- /gimport and /gexport ------------------------------------------------------------------

var (
	exportMu   sync.Mutex
	exportedAt time.Time
)

func cmdGimport(c *core.Ctx) error {
	c.L = i18n.English
	format := strings.ToLower(strings.TrimSpace(c.Args))
	if format != "" && !core.ValidBanFormat(format) {
		c.Say(c.T("gbans.use_gimport_csv_minicsv_json_human"))
		return nil
	}
	doc := c.Msg.Document
	if doc == nil {
		if r := core.RepliedMessage(c.Msg); r != nil {
			doc = r.Document
		}
	}
	if doc == nil {
		c.Say(c.T("gbans.reply_to_a_file_with_the"))
		return nil
	}
	if doc.FileSize > core.MaxImportBytes {
		c.Say(c.T("common.that_file_is_too_big_the"))
		return nil
	}
	status, err := c.ReplyText(c.T("gbans.importing_bans_please_wait"))
	if err != nil {
		return err
	}
	data, err := c.Download(doc.FileId)
	if err != nil {
		_ = c.EditSent(status, c.T("gbans.the_import_failed_nothing_more_was"), nil)
		return err
	}
	parsed, err := core.ParseBanImport(string(data), format)
	if err != nil {
		return c.EditSent(status, text.Esc(err.Error()), nil)
	}
	found := map[int64]core.GbanRow{}
	var order []int64
	for _, r := range parsed {
		r.Reason = strings.Join(strings.Fields(r.Reason), " ")
		if rs := []rune(r.Reason); len(rs) > maxGbanReason {
			r.Reason = string(rs[:maxGbanReason])
		}
		if r.Reason == "" {
			r.Reason = c.T("gbans.no_reason_given")
		}
		if _, dup := found[r.UserID]; !dup {
			found[r.UserID] = r
			order = append(order, r.UserID)
		}
	}
	var rows []core.GbanRow
	protected := 0
	for _, id := range order {
		prot, _ := c.App.IsProtected(c.Ctx, id)
		if prot || c.App.GetRole(c.Ctx, id) != core.NoRole {
			protected++
		} else {
			rows = append(rows, found[id])
		}
	}
	added := 0
	for start := 0; start < len(rows); start += importChunk {
		end := min(start+importChunk, len(rows))
		n, err := c.App.ImportGbans(c.Ctx, rows[start:end])
		if err != nil {
			_ = c.EditSent(status, c.T("gbans.the_import_failed_nothing_more_was"), nil)
			return err
		}
		added += n
	}
	skipped := len(rows) - added + protected
	msg := c.T("gbans.imported_ban", core.V{"added": added, "opt": plural(added)})
	if skipped > 0 {
		msg += c.T("common.skipped_already_banned_or_protected", core.V{"skipped": skipped})
	}
	_ = c.EditSent(status, msg, nil)
	admin := c.User
	c.App.RoleLog(c.Ctx, c.T("gbans.gbanimport_added_timestamp_admin", core.V{"added": added, "timestamp": core.Timestamp(time.Time{}),
		"id": text.UserRef(admin.Id, core.FullName(admin))}), nil)
	return nil
}

func cmdGexport(c *core.Ctx) error {
	c.L = i18n.English
	format := strings.ToLower(strings.TrimSpace(c.Args))
	if format == "" {
		format = "csv"
	}
	if !core.ValidBanFormat(format) {
		c.Say(c.T("gbans.use_gexport_csv_minicsv_json_or"))
		return nil
	}
	exportMu.Lock()
	if time.Since(exportedAt) < exportCooldown {
		exportMu.Unlock()
		c.Say(c.T("common.wait_a_little_before_exporting_again"))
		return nil
	}
	exportMu.Unlock()
	rows, err := c.App.AllGbans(c.Ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.Say(c.T("gbans.there_are_no_global_bans_to"))
		return nil
	}
	exportMu.Lock()
	exportedAt = time.Now()
	exportMu.Unlock()
	data, ext := core.RenderBanExport(rows, format)
	return c.SendDocument("gbans."+ext, data, c.T("gbans.global_bans", core.V{"rows": len(rows)}))
}

// --- Enforcement ----------------------------------------------------------------------------

// watchGbans removes globally banned users before any module sees what they sent (priority -100), and the banned
// among the newcomers of a "joined" message.
func watchGbans(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	chatID := m.Chat.Id
	if len(m.NewChatMembers) > 0 {
		removed := 0
		for _, u := range m.NewChatMembers {
			if u.Id == c.Bot.Id {
				continue
			}
			if banned, _ := c.App.IsGbanned(c.Ctx, u.Id); !banned {
				continue
			}
			if c.GbanRemovable(chatID, u.Id) {
				if enforceGban(c, chatID, u.Id, threadOf(m), core.FullName(&u)) {
					removed++
				}
			} else {
				slog.Info("Globally banned user joined and was left alone", "user", u.Id, "chat", chatID)
			}
		}
		if removed > 0 && removed == len(m.NewChatMembers) {
			return true, nil
		}
		return false, nil
	}
	if m.LeftChatMember != nil || c.User == nil {
		return false, nil // leaves are handled as member updates
	}
	if !c.Gbanned(chatID, c.User.Id) {
		return false, nil
	}
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, m.MessageId, nil)
	enforceGban(c, chatID, c.User.Id, threadOf(m), core.FullName(c.User))
	return true, nil
}

// watchGbanReaction removes a globally banned person who reacts to a message in a group.
func watchGbanReaction(c *core.Ctx, e *gotgbot.MessageReactionUpdated) (bool, error) {
	user := e.User
	if user == nil || !isGroup(e.Chat.Type) || user.Id == c.Bot.Id || !c.Gbanned(e.Chat.Id, user.Id) {
		return false, nil
	}
	enforceGban(c, e.Chat.Id, user.Id, 0, core.FullName(user))
	return true, nil
}

// watchGbanMember is the same for member updates. Joining or leaving counts, as in KryoRobot: a ban (by anyone) or
// another change is not acted on.
func watchGbanMember(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	if !isGroup(e.Chat.Type) {
		return false, nil
	}
	before, after := e.OldChatMember.MergeChatMember(), e.NewChatMember.MergeChatMember()
	user := after.User
	moved := (after.Status == "member" && before.Status != "member") || (after.Status == "left" && before.Status == "member")
	if user.Id == c.Bot.Id || !moved || !c.Gbanned(e.Chat.Id, user.Id) {
		return false, nil
	}
	enforceGban(c, e.Chat.Id, user.Id, 0, core.FullName(&user))
	return true, nil
}
