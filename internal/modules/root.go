package modules

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Core bot commands. They are for the people who run the bot and answer in English whatever
// language the chat speaks.

const (
	cbStaffHome   = "staff:home"
	cbStaffRole   = "staff:role:"
	staffTextMax  = 3800 // Telegram allows 4096 characters per message
	whitelistRole = core.Sudo
)

var started = time.Now()

type staffButton struct {
	role  core.Role
	title string
}

// /staff buttons, in display order.
var staffButtons = []staffButton{{core.Owner, "Owner"}, {core.Developer, "Developers"}, {core.Sudo, "Sudos"}, {core.Support, "Supports"}}

var (
	grantCommands  = map[string]core.Role{"adddev": core.Developer, "addsudo": core.Sudo, "addsupport": core.Support}
	revokeCommands = map[string]core.Role{"rmdev": core.Developer, "rmsudo": core.Sudo, "rmsupport": core.Support}
)

func registerRoot(r *core.Router) {
	core.RegisterInfo("root", false, func(c *core.Ctx, userID, _ int64) []core.InfoFlag {
		white, _ := c.App.IsWhitelisted(c.Ctx, userID)
		return []core.InfoFlag{{Label: c.T("root.immune"), Value: core.IsImmune(userID)}, {Label: c.T("root.whitelisted_2"), Value: white}}
	})
	r.Command(cmdPing, "ping")
	r.CommandRole(core.Developer, cmdModules, "modules")
	r.CommandRole(core.Developer, cmdModule, "module")
	r.CommandRole(core.Developer, cmdCleanchats, "cleanchats")
	r.CommandRole(core.Developer, cmdStats, "stats")
	r.CommandRole(core.Developer, cmdStatus, "status")
	r.CommandRole(core.Developer, cmdLeave, "leave")
	r.CommandRole(core.Developer, cmdSpeedtest, "speedtest")
	r.CommandRole(core.Support, cmdStaffRole, "adddev", "addsudo", "addsupport", "rmdev", "rmsudo", "rmsupport")
	r.CommandRole(core.Support, cmdStaff, "staff")
	r.ButtonRole(core.Support, cbStaffHome, cbStaffHomePress)
	r.ButtonRole(core.Support, cbStaffRole, cbStaffRolePress)
	r.CommandRole(whitelistRole, cmdWhitelist, "whitelist", "unwhitelist")
	// The deep link of the button /connect sends in a group. It lives here (a module that is always on) so a
	// switched-off connection module still answers why instead of treating it as a plain /start.
	r.Watch(-9, "connect link", watchConnectLink)
}

// --- /ping ----------------------------------------------------------------

func cmdPing(c *core.Ctx) error {
	// Latency is the round trip of sending a message to Telegram.
	t0 := time.Now()
	sent, err := c.ReplyText(c.T("root.pong"))
	if err != nil {
		return err
	}
	ms := float64(time.Since(t0)) / float64(time.Millisecond)
	return c.EditSent(sent, c.T("root.pong_latency_ms", core.V{"latency_ms": ms}), nil)
}

// --- Bot management ----------------------------------------------------------

func cmdModules(c *core.Ctx) error {
	mods := c.App.Modules()
	if len(mods) == 0 {
		c.Say(c.T("root.no_modules_loaded"))
		return nil
	}
	lines := []string{c.T("root.modules")}
	for _, m := range mods {
		status := c.T("common.disabled")
		if m.Enabled() {
			status = c.T("common.enabled")
		}
		line := fmt.Sprintf("<code>%s</code> - <b>%s</b>", text.Esc(m.Name), status)
		if m.Protected {
			line += c.T("root.protected")
		}
		lines = append(lines, line)
		if m.Description != "" {
			lines = append(lines, fmt.Sprintf("<i>Description: %s</i>", text.Esc(m.Description)))
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdModule(c *core.Ctx) error {
	args := strings.Fields(c.Args)
	action := ""
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	if len(args) != 2 || (action != "enable" && action != "disable") {
		c.Say(c.T("root.usage_module_lt_enable_disable_gt"))
		return nil
	}
	name := args[1]
	found, err := c.App.SetModuleEnabled(c.Ctx, name, action == "enable")
	if err != nil {
		if core.IsProtectedError(err) {
			c.Say(c.T("root.module_is_protected_and_can_t", core.V{"lower": text.Esc(strings.ToLower(name))}))
			return nil
		}
		return err
	}
	if !found {
		c.Say(c.T("root.module_not_found", core.V{"name": text.Esc(name)}))
		return nil
	}
	past := c.T("common.disabled")
	if action == "enable" {
		past = c.T("common.enabled")
	}
	ended := ""
	if action == "disable" && strings.EqualFold(name, "connection") {
		ended = endAllConnections(c)
	}
	c.Say(c.T("root.module", core.V{"lower": text.Esc(strings.ToLower(name)), "past": past, "ended": ended}))
	return nil
}

// endAllConnections: connections can't outlive the module; end them all and tell the people on them.
func endAllConnections(c *core.Ctx) string {
	open, err := c.App.DropAllConnections(c.Ctx)
	if err != nil {
		return c.T("root.ending_the_open_connections_failed_they")
	}
	for _, item := range open {
		title := item.Title
		if title == "" {
			title = strconv.FormatInt(item.ChatID, 10)
		}
		_ = c.SayTo(item.UserID, c.T("root.your_connection_to_has_ended_connections", core.V{"v": text.Esc(title)}))
	}
	n := len(open)
	if n == 0 {
		return ""
	}
	opt := "s"
	if n == 1 {
		opt = ""
	}
	return c.T("root.connection_ended", core.V{"n": n, "opt": opt})
}

func cmdCleanchats(c *core.Ctx) error {
	if c.App.IsCleaning() {
		c.Say(c.T("root.a_chat_cleanup_is_already_running"))
		return nil
	}
	sent, err := c.ReplyText(c.T("root.checking_chats_this_can_take_a"))
	if err != nil {
		return err
	}
	result, err := c.App.CleanChats(c.Ctx)
	if err != nil {
		return err
	}
	if result == nil { // someone else started one in the meantime
		return c.EditSent(sent, c.T("root.a_chat_cleanup_is_already_running"), nil)
	}
	return c.EditSent(sent, core.CleanupReport(result, c.T("root.chat_cleanup_finished")), nil)
}

func cmdStats(c *core.Ctx) error {
	db := c.App.DB
	var users, bots, staff, approved, feds int64
	if err := db.Row(c.Ctx, "SELECT count(*), count(*) FILTER (WHERE is_bot) FROM users", nil, &users, &bots); err != nil {
		return err
	}
	rows, err := db.Pool.Query(c.Ctx, "SELECT type, count(*) FROM chats GROUP BY type")
	if err != nil {
		return err
	}
	byType := map[string]int64{}
	var all int64
	for rows.Next() {
		var t string
		var n int64
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return err
		}
		byType[t] = n
		all += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := db.Row(c.Ctx, "SELECT count(*) FROM roles", nil, &staff); err != nil {
		return err
	}
	if err := db.Row(c.Ctx, "SELECT count(*) FROM approvals", nil, &approved); err != nil {
		return err
	}
	// Global bans and federations arrive with their modules; until then there are none.
	var gbans int
	feds = 0
	if f := core.BanLookups.GbanCount; f != nil {
		if gbans, err = f(c.Ctx); err != nil {
			return err
		}
	}
	if f := core.BanLookups.FedCount; f != nil {
		n, err := f(c.Ctx)
		if err != nil {
			return err
		}
		feds = int64(n)
	}
	white, err := c.App.WhitelistAll(c.Ctx)
	if err != nil {
		return err
	}
	loaded, enabled := c.App.CountModules()
	lines := []string{
		c.T("root.bot_statistics"),
		"",
		c.T("root.known_users_bots", core.V{"total": users, "bots": bots}),
		c.T("root.chats_groups_channels", core.V{"sum": all, "groups": byType["group"] + byType["supergroup"], "get": byType["channel"]}),
		c.T("root.global_bans", core.V{"v": gbans}),
		c.T("root.federations", core.V{"total": feds}),
		c.T("root.whitelisted", core.V{"v": len(white)}),
		c.T("root.approved_users", core.V{"total": approved}),
		c.T("root.staff_including_the_owner", core.V{"v": staff + 1}),
		c.T("root.modules_of_enabled", core.V{"count_enabled": enabled, "count_loaded": loaded}),
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

// memoryMB is the memory the bot process uses, from /proc (Linux); ok is false elsewhere.
func memoryMB() (float64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseFloat(f[1], 64); err == nil {
					return kb / 1024, true
				}
			}
		}
	}
	return 0, false
}

func cmdStatus(c *core.Ctx) error {
	ms := func(t time.Time) float64 { return float64(time.Since(t)) / float64(time.Millisecond) }
	t0 := time.Now()
	telegram := ""
	if _, err := c.Bot.GetMeWithContext(c.Ctx, nil); err == nil {
		telegram = c.T("root.ms_2", core.V{"telegram": ms(t0)})
	} else {
		telegram = c.T("root.not_reachable")
	}
	t0 = time.Now()
	var one int
	dbText := c.T("root.not_reachable")
	if err := c.App.DB.Row(c.Ctx, "SELECT 1", nil, &one); err == nil {
		dbText = c.T("root.ms", core.V{"v": ms(t0)})
	}
	memory := c.T("root.unknown")
	if mb, ok := memoryMB(); ok {
		memory = c.T("root.mb", core.V{"memory": mb})
	}
	loaded, enabled := c.App.CountModules()
	lines := []string{
		c.T("root.bot_status"),
		"",
		c.T("root.uptime", core.V{"duration_text": text.DurationText(c.L, int(time.Since(started).Seconds()))}),
		c.T("root.telegram") + telegram,
		c.T("root.database", core.V{"db_text": dbText}),
		c.T("root.memory") + memory,
		c.T("root.modules_of_enabled", core.V{"count_enabled": enabled, "count_loaded": loaded}),
		// The versions of Go and of the bot library.
		"• <b>Go:</b> <code>" + text.Esc(runtime.Version()) + "</code>",
		"• <b>gotgbot:</b> <code>v2</code>",
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdLeave(c *core.Ctx) error {
	if c.Msg.Chat.Type == "private" {
		c.Say(c.T("root.i_can_only_leave_groups_and"))
		return nil
	}
	chatID := c.Msg.Chat.Id
	c.Say(c.T("root.farewell_my_duties_here_are_finished"))
	if _, err := c.Bot.LeaveChatWithContext(c.Ctx, chatID, nil); err != nil {
		why := err.Error()
		if tg := core.TgError(err); tg != nil {
			why = tg.Description
		}
		c.Say(c.T("root.i_couldn_t_leave_this_chat", core.V{"getattr": text.Esc(why)}))
		return nil
	}
	return c.App.RemoveChat(c.Ctx, chatID)
}

var (
	speedMu      sync.Mutex
	speedRunning bool
	// SpeedtestBase is where /speedtest measures; tests point it at a local server.
	SpeedtestBase = core.SpeedtestURL
)

func cmdSpeedtest(c *core.Ctx) error {
	speedMu.Lock()
	if speedRunning {
		speedMu.Unlock()
		c.Say(c.T("root.a_speedtest_is_already_running"))
		return nil
	}
	speedRunning = true
	speedMu.Unlock()
	defer func() { speedMu.Lock(); speedRunning = false; speedMu.Unlock() }()

	sent, err := c.ReplyText(c.T("root.running_a_speedtest_this_takes_a"))
	if err != nil {
		return err
	}
	var msg string
	if ping, down, up, err := core.Speedtest(c.Ctx, SpeedtestBase); err != nil {
		msg = c.T("root.speedtest_failed", core.V{"v": text.Esc(err.Error())})
	} else {
		msg = c.T("root.speedtest_ping_ms_download_mbps_upload", core.V{"ping": ping, "download": down, "upload": up})
	}
	return c.EditSent(sent, msg, nil)
}

// --- Staff roles ----------------------------------------------------------------

func cmdStaffRole(c *core.Ctx) error {
	granting := false
	targetRole, ok := grantCommands[c.Cmd]
	if ok {
		granting = true
	} else if targetRole, ok = revokeCommands[c.Cmd]; !ok {
		return nil
	}
	actor := c.User
	if !core.CanManage(c.Role, targetRole) {
		c.Say(c.T("root.you_don_t_have_permission_to", core.V{"label": core.RoleLabel(targetRole, true)}))
		return nil
	}
	target, err := c.ResolveTarget(false, false)
	if err != nil {
		return finish(c, err)
	}
	if target.ID == actor.Id {
		c.Say(c.T("root.you_can_t_change_your_own"))
		return nil
	}
	if target.ID == c.App.Cfg.OwnerID {
		c.Say(c.T("common.the_owner_s_role_can_t"))
		return nil
	}
	who := text.UserLink(target.ID, target.Name)
	current := c.App.GetRole(c.Ctx, target.ID)
	label := core.RoleLabel(targetRole, false)
	actorName := core.FullName(actor)

	if granting {
		switch {
		case current == targetRole:
			c.Say(c.T("root.is_already", core.V{"who": who, "label": label}))
			return nil
		case current != core.NoRole:
			c.Say(c.T("root.is_already_remove_that_role_first", core.V{"who": who, "label": core.RoleLabel(current, false)}))
			return nil
		}
		if f := core.BanLookups.Gbanned; f != nil {
			banned, err := f(c.Ctx, target.ID)
			if err != nil {
				return err
			}
			if banned {
				c.Say(c.T("root.is_globally_banned_and_can_t", core.V{"who": who}))
				return nil
			}
		}
		if f := core.BanLookups.FedBans; f != nil {
			names, err := f(c.Ctx, target.ID)
			if err != nil {
				return err
			}
			if len(names) > 0 {
				shown := names
				if len(shown) > 5 {
					shown = shown[:5]
				}
				esc := make([]string, len(shown))
				for i, n := range shown {
					esc[i] = text.Esc(n)
				}
				more := ""
				if len(names) > 5 {
					more = c.T("root.and_more", core.V{"v": len(names) - 5})
				}
				c.Say(c.T("root.is_banned_in_a_federation_and", core.V{"who": who, "names": strings.Join(esc, ", "), "more": more}))
				return nil
			}
		}
		if err := c.App.SetRole(c.Ctx, target.ID, targetRole, actor.Id, target.Name, actorName); err != nil {
			return err
		}
		c.Say(c.T("root.has_been_promoted_to", core.V{"who": who, "label": label}))
	} else {
		if current != targetRole {
			c.Say(c.T("root.is_not", core.V{"who": who, "label": label}))
			return nil
		}
		if err := c.App.RemoveRole(c.Ctx, target.ID); err != nil {
			return err
		}
		c.Say(c.T("root.has_been_removed_from", core.V{"who": who, "label": label}))
	}
	tags := core.RoleLogTags[targetRole]
	tag := tags[1]
	if granting {
		tag = tags[0]
	}
	c.App.RoleLog(c.Ctx, core.AuditText(tag, target.ID, target.Name, actor.Id, actorName), nil)
	return nil
}

func staffHomeKeyboard() *gotgbot.InlineKeyboardMarkup {
	var buttons []gotgbot.InlineKeyboardButton
	for _, b := range staffButtons {
		buttons = append(buttons, core.Button(b.title, cbStaffRole+strconv.Itoa(int(b.role))))
	}
	return core.Keyboard(buttons[:2], buttons[2:])
}

func staffBackKeyboard(c *core.Ctx) *gotgbot.InlineKeyboardMarkup {
	return core.Keyboard([]gotgbot.InlineKeyboardButton{core.Button(c.T("common.back"), cbStaffHome)})
}

func staffListText(c *core.Ctx, role core.Role) (string, error) {
	title := ""
	for _, b := range staffButtons {
		if b.role == role {
			title = b.title
		}
	}
	var rows []core.StaffMember
	if role == core.Owner {
		rows = []core.StaffMember{{UserID: c.App.Cfg.OwnerID}}
	} else {
		var err error
		if rows, err = c.App.ListRole(c.Ctx, role); err != nil {
			return "", err
		}
	}
	if len(rows) == 0 {
		return c.T("root.nobody_holds_this_role_yet", core.V{"title": title}), nil
	}

	// Look every person up once, in parallel; fall back to the stored name.
	stored := map[int64]string{}
	var ids []int64
	remember := func(id int64, name string) {
		if _, seen := stored[id]; !seen {
			stored[id] = name
			ids = append(ids, id)
		}
	}
	for _, row := range rows {
		remember(row.UserID, row.Name)
		if row.AddedBy != 0 {
			remember(row.AddedBy, row.AddedByName)
		}
	}
	names := make(map[int64]string, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := c.DisplayName(id, stored[id])
			mu.Lock()
			names[id] = n
			mu.Unlock()
		}()
	}
	wg.Wait()

	lines := []string{fmt.Sprintf("<b>%s</b> (%d)", title, len(rows))}
	shown := 0
	for i, row := range rows {
		entry := []string{fmt.Sprintf("%d. %s", i+1, text.UserRef(row.UserID, names[row.UserID]))}
		if row.AddedAt != nil {
			entry = append(entry, "   Added: "+core.Timestamp(*row.AddedAt))
		}
		if row.AddedBy != 0 {
			entry = append(entry, "   By: "+text.UserRef(row.AddedBy, names[row.AddedBy]))
		}
		block := strings.Join(entry, "\n")
		size := 0
		for _, l := range lines {
			size += utf8.RuneCountInString(l) + 1
		}
		if size+utf8.RuneCountInString(block) > staffTextMax {
			lines = append(lines, fmt.Sprintf("\n...and %d more.", len(rows)-shown))
			break
		}
		lines = append(lines, "\n"+block)
		shown++
	}
	return strings.Join(lines, "\n"), nil
}

func cmdStaff(c *core.Ctx) error {
	_, err := c.Reply(c.T("root.staff_choose_a_role_to_see"), staffHomeKeyboard())
	return err
}

func cbStaffHomePress(c *core.Ctx) error {
	c.Answer("", false)
	if c.Msg == nil || c.Cb.Data != cbStaffHome {
		return nil
	}
	return c.Edit(c.T("root.staff_choose_a_role_to_see"), staffHomeKeyboard())
}

func cbStaffRolePress(c *core.Ctx) error {
	n, err := strconv.Atoi(strings.TrimPrefix(c.Cb.Data, cbStaffRole))
	role := core.Role(n)
	if err != nil || role < core.Support || role > core.Owner {
		c.Answer(c.T("root.unknown_role"), true)
		return nil
	}
	c.Answer("", false)
	if c.Msg == nil {
		return nil
	}
	msg, err := staffListText(c, role)
	if err != nil {
		return err
	}
	return c.Edit(msg, staffBackKeyboard(c))
}

// --- Whitelist --------------------------------------------------------------------

func cmdWhitelist(c *core.Ctx) error {
	adding := c.Cmd == "whitelist"
	actor := c.User
	target, err := c.ResolveTarget(true, false)
	if err != nil {
		return finish(c, err)
	}
	if target.ID == actor.Id {
		c.Say(c.T("root.you_can_t_change_your_own_2"))
		return nil
	}
	who := text.UserLink(target.ID, target.Name)
	if core.IsImmune(target.ID) {
		c.Say(c.T("root.is_immune_to_all_bot_actions", core.V{"who": who}))
		return nil
	}
	var tag string
	if adding {
		if f := core.BanLookups.Gbanned; f != nil {
			banned, err := f(c.Ctx, target.ID)
			if err != nil {
				return err
			}
			if banned {
				c.Say(c.T("root.is_globally_banned_and_can_t_2", core.V{"who": who}))
				return nil
			}
		}
		added, err := c.App.WhitelistAdd(c.Ctx, target.ID, actor.Id, target.Name, core.FullName(actor))
		if err != nil {
			return err
		}
		if !added {
			c.Say(c.T("root.is_already_whitelisted", core.V{"who": who}))
			return nil
		}
		tag = "#WHITELIST"
		c.Say(c.T("root.has_been_whitelisted", core.V{"who": who}))
	} else {
		removed, err := c.App.WhitelistRemove(c.Ctx, target.ID)
		if err != nil {
			return err
		}
		if !removed {
			c.Say(c.T("root.is_not_whitelisted", core.V{"who": who}))
			return nil
		}
		tag = "#UNWHITELIST"
		c.Say(c.T("root.has_been_removed_from_the_whitelist", core.V{"who": who}))
	}
	c.App.RoleLog(c.Ctx, core.AuditText(tag, target.ID, target.Name, actor.Id, core.FullName(actor)), nil)
	return nil
}
