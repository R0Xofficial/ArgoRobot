package modules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Everyday commands: /start, /help, /id, /info (modules/misc.py). The lookups (/tr, /dict, /time, /weather, /cash) are
// in lookups.go.

const (
	cbStart       = "start:home"
	cbStartLegacy = "root:start" // buttons on messages sent before start moved here
	cbHelp        = "help:home"
	cbHelpModule  = "help:module:"
	cbHelpSection = "help:section:"
	cbHelpPage    = "help:page:"
	cbHelpMore    = "help:more:" // help:more:<module>:<section number or ->:<page number>

	// A module page up to this long stays one page; a longer one keeps its notes behind buttons, and when even its
	// commands are too much they get pages of their own.
	singleLimit = 2000
	mainLimit   = 2400
	partLimit   = 1800
)

// helpPages are the extra reference pages of some modules.
var helpPages = map[string][]core.HelpPage{
	"locks": {{Label: "locks.page.lock_types", Text: lockTypesPages}},
}

func registerMisc(r *core.Router) {
	r.Command(cmdStart, "start")
	r.Command(cmdHelp, "help")
	r.Command(cmdID, "id")
	r.Command(cmdInfo, "info")
	r.Button("start:", cbStartHome)
	r.Button(cbStartLegacy, cbStartHome)
	r.Button("help:", cbHelpAny)
	registerLookups(r)
}

// --- Texts and keyboards ----------------------------------------------------

func displayName(c *core.Ctx) string {
	if c.Msg.From != nil {
		return core.FullName(c.Msg.From)
	}
	return c.T("misc.there")
}

func startText(c *core.Ctx, name string) string {
	t := c.T("misc.hello_i_m_argo_i_m", core.V{"name": text.Esc(name)})
	if support := atName(c.App.Cfg.SupportChatUsername); support != "" {
		t += c.T("misc.questions_bugs_or_appeals_join_the", core.V{"support": text.Esc(support)})
	}
	return t
}

func helpHomeText(c *core.Ctx) string {
	var prefixes []string
	for _, p := range c.App.Cfg.CommandPrefix {
		prefixes = append(prefixes, "<code>"+text.Esc(string(p))+"</code>")
	}
	lines := []string{
		c.T("misc.help"), "",
		c.T("misc.pick_a_category_below_to_see"), "",
		c.T("misc.useful_commands"),
		c.T("misc.start_say_hello_in_a_private"),
		c.T("misc.help_open_this_panel_help_notes"), "",
		c.T("misc.settings_that_take_on_or_off"),
		c.T("misc.commands_can_start_with", core.V{"prefixes": strings.Join(prefixes, " ")}),
	}
	if name := atName(c.App.Cfg.SupportChatUsername); name != "" {
		lines = append(lines, "", c.T("misc.found_a_bug_or_something_doesn", core.V{"name": text.Esc(name)}))
	}
	return strings.Join(lines, "\n")
}

type helpModule struct {
	info core.ModuleInfo
	help *core.Help
}

func (m helpModule) title(c *core.Ctx) string {
	if m.help.Title != "" {
		return c.T(m.help.Title)
	}
	return c.L.Or("help.title."+m.info.Name, strings.ToUpper(m.info.Name[:1])+m.info.Name[1:])
}

func entryVisible(e core.HelpEntry, role core.Role) bool {
	return e.MinRole == 0 || int(role) >= e.MinRole
}

func (m helpModule) entries(role core.Role) []core.HelpEntry {
	var out []core.HelpEntry
	for _, e := range m.help.Entries {
		if entryVisible(e, role) {
			out = append(out, e)
		}
	}
	return out
}

// sections are the sections of a module in the order they first appear.
func (m helpModule) sections() []string {
	var out []string
	for _, e := range m.help.Entries {
		if e.Section != "" && !contains(out, e.Section) {
			out = append(out, e.Section)
		}
	}
	return out
}

type numbered struct {
	Number int
	Name   string
}

// visibleSections are the sections that have a command this role may see. The number is stable.
func (m helpModule) visibleSections(role core.Role) []numbered {
	shown := map[string]bool{}
	for _, e := range m.entries(role) {
		if e.Section != "" {
			shown[e.Section] = true
		}
	}
	var out []numbered
	for i, name := range m.sections() {
		if shown[name] {
			out = append(out, numbered{i, name})
		}
	}
	return out
}

type detail struct{ usage, description, who string }

// detailed is entries with who may use each: only the commands of the section, or (no section) those that have none.
func (m helpModule) detailed(c *core.Ctx, role core.Role, section string) []detail {
	var out []detail
	for _, e := range m.entries(role) {
		if e.Section != section {
			continue
		}
		label := e.Label
		if label == "" && e.MinRole > 0 {
			title := core.Role(e.MinRole).String()
			out = append(out, detail{e.Usage, e.Description, c.T("help_index.and_up", core.V{"title": title})})
			continue
		}
		if label == "" {
			label = m.help.Who
		}
		out = append(out, detail{e.Usage, e.Description, label})
	}
	return out
}

// helpCategories: enabled modules that have at least one command visible to the role; the protected ones first.
func helpCategories(c *core.Ctx, role core.Role) []helpModule {
	var out []helpModule
	for _, info := range c.App.Modules() {
		h := helpData[info.Name]
		if h == nil || !info.Enabled() {
			continue
		}
		if m := (helpModule{info, h}); len(m.entries(role)) > 0 {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].info.Protected != out[j].info.Protected {
			return out[i].info.Protected
		}
		return out[i].info.Name < out[j].info.Name
	})
	return out
}

func helpModuleByName(c *core.Ctx, name string, role core.Role) (helpModule, bool) {
	info := c.App.Module(name)
	if info == nil || !info.Enabled() || helpData[name] == nil {
		return helpModule{}, false
	}
	m := helpModule{*info, helpData[name]}
	return m, len(m.entries(role)) > 0
}

// cleandoc is inspect.cleandoc: the indentation of a text and the blank lines around it are removed.
func cleandoc(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\t", "        "), "\n")
	margin := -1
	for _, l := range lines[min(1, len(lines)):] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); margin < 0 || n < margin {
			margin = n
		}
	}
	lines[0] = strings.TrimLeft(lines[0], " ")
	if margin > 0 {
		for i := 1; i < len(lines); i++ {
			if len(lines[i]) >= margin {
				lines[i] = lines[i][margin:]
			} else {
				lines[i] = strings.TrimLeft(lines[i], " ")
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// paragraphs: plain text from a module (about, notes) made safe for HTML; blank lines stay.
func paragraphs(s string) string {
	lines := strings.Split(cleandoc(s), "\n")
	for i, l := range lines {
		lines[i] = text.Esc(strings.TrimRight(l, " \t\r\f\v"))
	}
	return strings.Join(lines, "\n")
}

type extraPage struct{ label, html string }

// helpParts is the page of one module and the extra pages behind its buttons. The page says what the module is, then
// lists the commands this person may use. A module with sections shows the commands that belong to none here, and a
// page per section. A page that gets long keeps its notes behind buttons, and its commands too when they alone are
// too much.
func helpParts(c *core.Ctx, m helpModule, role core.Role, section int) (string, []extraPage) {
	names := m.sections()
	chosen := ""
	if section >= 0 && section < len(names) {
		chosen = names[section]
	}
	title := m.title(c)
	pageTitle := "<b>" + text.Esc(title) + "</b>"
	if chosen != "" {
		pageTitle = "<b>" + text.Esc(title) + " - " + text.Esc(c.T(chosen)) + "</b>"
	}
	head := []string{pageTitle}
	if chosen == "" {
		if m.info.Description != "" {
			head = append(head, "<i>"+text.Esc(c.L.Or("help.description."+m.info.Name, m.info.Description))+"</i>")
		}
		if m.help.About != "" {
			head = append(head, "", paragraphs(c.T(m.help.About)))
		}
	}
	var blocks []string
	for _, d := range m.detailed(c, role, chosen) {
		line := "<code>" + text.Esc(d.usage) + "</code>"
		if d.who != "" {
			line += "  <i>(" + text.Esc(c.T(d.who)) + ")</i>"
		}
		blocks = append(blocks, line+"\n"+text.Esc(c.T(d.description)))
	}
	var hint []string
	if chosen == "" && len(m.visibleSections(role)) > 0 {
		hint = []string{"", c.T("misc.pick_a_section_below_for_the")}
	}
	type note struct{ title, text string }
	var notes []note
	for _, n := range m.help.Notes {
		if n.Section == chosen {
			notes = append(notes, note{c.T(n.Title), c.T(n.Text)})
		}
	}
	commands := func(items []string) []string {
		if len(items) == 0 {
			return nil
		}
		return append([]string{"", "<b>" + c.T("misc.commands") + "</b>", ""}, strings.Split(strings.Join(items, "\n\n"), "\n")...)
	}
	concat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	var noteLines []string
	for _, n := range notes {
		noteLines = append(noteLines, "", "<b>"+text.Esc(n.title)+"</b>", paragraphs(n.text))
	}
	full := strings.Join(concat(head, commands(blocks), hint, noteLines), "\n")
	if utf8.RuneCountInString(full) <= singleLimit {
		return full, nil
	}
	opening := strings.Replace(pageTitle, "</b>", "", 1)
	var extra []extraPage
	for _, n := range notes {
		extra = append(extra, extraPage{n.title, opening + " - " + text.Esc(n.title) + "</b>\n\n" + paragraphs(n.text)})
	}
	core2 := strings.Join(concat(head, commands(blocks), hint), "\n")
	if utf8.RuneCountInString(core2) <= mainLimit {
		return core2, extra
	}
	var chunks [][]string
	var current []string
	for _, b := range blocks {
		if len(current) > 0 && utf8.RuneCountInString(strings.Join(append(append([]string{}, current...), b), "\n\n")) > partLimit {
			chunks = append(chunks, current)
			current = nil
		}
		current = append(current, b)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	var pages []extraPage
	for i, chunk := range chunks {
		label := c.T("misc.commands")
		if len(chunks) != 1 {
			label = c.T("misc.commands_2", core.V{"number": i + 1})
		}
		pages = append(pages, extraPage{label, opening + " - " + label + "</b>\n\n" + strings.Join(chunk, "\n\n")})
	}
	return strings.Join(concat(head, hint), "\n"), append(pages, extra...)
}

func btn(label, data string) gotgbot.InlineKeyboardButton { return core.Button(label, data) }

func startKeyboard(c *core.Ctx) *gotgbot.InlineKeyboardMarkup {
	var row []gotgbot.InlineKeyboardButton
	if u := c.App.Cfg.GithubURL; u != "" {
		row = append(row, gotgbot.InlineKeyboardButton{Text: c.T("misc.github"), Url: u})
	}
	row = append(row, btn(c.T("misc.help_2"), cbHelp))
	return core.Keyboard(row)
}

func helpHomeKeyboard(c *core.Ctx, role core.Role) *gotgbot.InlineKeyboardMarkup {
	var buttons []gotgbot.InlineKeyboardButton
	for _, m := range helpCategories(c, role) {
		buttons = append(buttons, btn(m.title(c), cbHelpModule+m.info.Name))
	}
	// Three to a row. A last row of two or one is stretched over the whole width by Telegram.
	var rows [][]gotgbot.InlineKeyboardButton
	for i := 0; i < len(buttons); i += 3 {
		rows = append(rows, buttons[i:min(i+3, len(buttons))])
	}
	rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T("common.back"), cbStart)})
	return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// moreRows are the buttons of a page's extra pages (long command lists, notes), two to a row.
func moreRows(c *core.Ctx, m helpModule, role core.Role, section int) [][]gotgbot.InlineKeyboardButton {
	where := "-"
	if section >= 0 {
		where = strconv.Itoa(section)
	}
	_, extra := helpParts(c, m, role, section)
	var buttons []gotgbot.InlineKeyboardButton
	for i, e := range extra {
		buttons = append(buttons, btn(e.label, fmt.Sprintf("%s%s:%s:%d", cbHelpMore, m.info.Name, where, i)))
	}
	var rows [][]gotgbot.InlineKeyboardButton
	for i := 0; i < len(buttons); i += 2 {
		rows = append(rows, buttons[i:min(i+2, len(buttons))])
	}
	return rows
}

// helpCategoryKeyboard goes back to the list of categories, or to the module's page from one of its sections.
func helpCategoryKeyboard(c *core.Ctx, m *helpModule, role core.Role, section int) *gotgbot.InlineKeyboardMarkup {
	if m != nil && section >= 0 {
		rows := moreRows(c, *m, role, section)
		rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T("common.back"), cbHelpModule+m.info.Name)})
		return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	var rows [][]gotgbot.InlineKeyboardButton
	if m != nil {
		for _, s := range m.visibleSections(role) {
			rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T(s.Name), fmt.Sprintf("%s%s:%d", cbHelpSection, m.info.Name, s.Number))})
		}
		rows = append(rows, moreRows(c, *m, role, -1)...)
		for i, p := range helpPages[m.info.Name] {
			rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T(p.Label), fmt.Sprintf("%s%s:%d:0", cbHelpPage, m.info.Name, i))})
		}
	}
	rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T("common.back"), cbHelp)})
	return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func helpPageKeyboard(c *core.Ctx, m helpModule, number, part, total int) *gotgbot.InlineKeyboardMarkup {
	var rows [][]gotgbot.InlineKeyboardButton
	if total > 1 {
		var move []gotgbot.InlineKeyboardButton
		if part > 0 {
			move = append(move, btn(c.T("misc.previous"), fmt.Sprintf("%s%s:%d:%d", cbHelpPage, m.info.Name, number, part-1)))
		}
		if part < total-1 {
			move = append(move, btn(c.T("misc.next"), fmt.Sprintf("%s%s:%d:%d", cbHelpPage, m.info.Name, number, part+1)))
		}
		rows = append(rows, move)
	}
	rows = append(rows, []gotgbot.InlineKeyboardButton{btn(c.T("common.back"), cbHelpModule+m.info.Name)})
	return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// --- /start -----------------------------------------------------------------

func cmdStart(c *core.Ctx) error {
	if c.Msg.Chat.Type != "private" {
		// Groups: greet and point to the private chat. No buttons.
		c.Say(c.T("misc.hello_i_m_argo_i_d", core.V{"name": text.Esc(displayName(c)), "username": text.Esc(c.Bot.Username)}))
		return nil
	}
	// Deep link from the group's /help button: t.me/<bot>?start=help
	payload := strings.ToLower(strings.TrimSpace(c.Args))
	role := c.Role
	switch {
	case payload == "help":
		_, err := c.Reply(helpHomeText(c), helpHomeKeyboard(c, role))
		return err
	case strings.HasPrefix(payload, "connect_"):
		// t.me/<bot>?start=connect_<chat id>: the button /connect sends in a group
		if id, err := strconv.ParseInt(payload[len("connect_"):], 10, 64); err == nil && isSignedDigits(payload[len("connect_"):]) {
			return connectAndTell(c, id)
		}
	case strings.HasPrefix(payload, "cap_") && isSignedDigits(payload[4:]):
		// t.me/<bot>?start=cap_<id>: the captcha button of a welcome message
		id, _ := strconv.ParseInt(payload[4:], 10, 64)
		if problem := c.OpenCaptchaInPrivate(c.Msg.From, id, c.Msg.Chat.Id); problem != "" {
			c.Say(problem)
		}
		return nil
	case strings.HasPrefix(payload, "help_"):
		if m, ok := helpModuleByName(c, payload[len("help_"):], role); ok {
			page, _ := helpParts(c, m, role, -1)
			_, err := c.Reply(page, helpCategoryKeyboard(c, &m, role, -1))
			return err
		}
		_, err := c.Reply(helpHomeText(c), helpHomeKeyboard(c, role))
		return err
	}
	_, err := c.Reply(startText(c, displayName(c)), startKeyboard(c))
	return err
}

func isSignedDigits(s string) bool {
	s = strings.TrimLeft(s, "-")
	return s != "" && isDigits(s)
}

func cbStartHome(c *core.Ctx) error {
	if c.Cb.Data != cbStart && c.Cb.Data != cbStartLegacy {
		return nil
	}
	c.Answer("", false) // always answer first, otherwise the button keeps spinning
	if c.Msg != nil {
		return c.Edit(startText(c, core.FullName(c.User)), startKeyboard(c))
	}
	return nil
}

// --- /help ------------------------------------------------------------------

// helpTopic is the help page a typed word stands for: a category by its name or title, else the category that has a
// command of that name (/help save opens the notes).
func helpTopic(c *core.Ctx, topic string, role core.Role) (helpModule, bool) {
	word := strings.ToLower(strings.Join(strings.Fields(strings.TrimLeft(strings.ToLower(topic), "/!")), " "))
	if word == "" {
		return helpModule{}, false
	}
	found := helpCategories(c, role)
	for _, m := range found {
		if word == strings.ToLower(m.info.Name) || word == strings.ToLower(m.title(c)) {
			return m, true
		}
	}
	var starts []helpModule
	for _, m := range found {
		if strings.HasPrefix(strings.ToLower(m.info.Name), word) || strings.HasPrefix(strings.ToLower(m.title(c)), word) {
			starts = append(starts, m)
		}
	}
	if len(starts) == 1 {
		return starts[0], true
	}
	command := strings.Fields(word)[0]
	for _, m := range found {
		for _, e := range m.entries(role) {
			first := strings.Fields(e.Usage)
			if len(first) == 0 {
				continue
			}
			name := strings.TrimLeft(first[0], "/!")
			name = strings.SplitN(strings.SplitN(name, "[", 2)[0], "<", 2)[0]
			if strings.ToLower(name) == command {
				return m, true
			}
		}
	}
	return helpModule{}, false
}

func cmdHelp(c *core.Ctx) error {
	topic := strings.TrimSpace(c.Args)
	role := c.Role
	var m helpModule
	found := false
	if topic != "" {
		if m, found = helpTopic(c, topic, role); !found {
			c.Say(c.T("misc.i_have_no_help_page_for", core.V{"v": text.Esc(text.Truncate(topic, 40, ""))}))
			return nil
		}
	}
	if c.Msg.Chat.Type != "private" {
		// Groups: a button that opens the help panel (or the asked-for page) in the private chat.
		suffix := "help"
		if found {
			suffix = "help_" + m.info.Name
		}
		_, err := c.Reply(c.T("misc.click_the_button_below_to_see"), core.Keyboard(
			[]gotgbot.InlineKeyboardButton{{Text: c.T("misc.help_2"), Url: "https://t.me/" + c.Bot.Username + "?start=" + suffix}}))
		return err
	}
	if found {
		page, _ := helpParts(c, m, role, -1)
		_, err := c.Reply(page, helpCategoryKeyboard(c, &m, role, -1))
		return err
	}
	_, err := c.Reply(helpHomeText(c), helpHomeKeyboard(c, role))
	return err
}

// cbHelpAny dispatches the buttons of the help panel.
func cbHelpAny(c *core.Ctx) error {
	data := c.Cb.Data
	role := c.Role
	switch {
	case data == cbHelp:
		c.Answer("", false)
		if c.Msg != nil {
			return c.Edit(helpHomeText(c), helpHomeKeyboard(c, role))
		}
	case strings.HasPrefix(data, cbHelpModule):
		m, ok := helpModuleByName(c, data[len(cbHelpModule):], role)
		if !ok { // buttons on old messages can point to a module that is disabled now
			c.Answer(c.T("misc.this_category_is_not_available"), true)
			return nil
		}
		c.Answer("", false)
		if c.Msg != nil {
			page, _ := helpParts(c, m, role, -1)
			return c.Edit(page, helpCategoryKeyboard(c, &m, role, -1))
		}
	case strings.HasPrefix(data, cbHelpSection):
		rest := data[len(cbHelpSection):]
		i := strings.LastIndex(rest, ":")
		if i < 0 {
			c.Answer(c.T("misc.this_section_is_not_available"), true)
			return nil
		}
		number, err := strconv.Atoi(rest[i+1:])
		m, ok := helpModuleByName(c, rest[:i], role)
		shown := false
		if ok && err == nil {
			for _, s := range m.visibleSections(role) {
				shown = shown || s.Number == number
			}
		}
		if !shown {
			c.Answer(c.T("misc.this_section_is_not_available"), true)
			return nil
		}
		c.Answer("", false)
		if c.Msg != nil {
			page, _ := helpParts(c, m, role, number)
			return c.Edit(page, helpCategoryKeyboard(c, &m, role, number))
		}
	case strings.HasPrefix(data, cbHelpMore):
		parts := strings.SplitN(data[len(cbHelpMore):], ":", 3)
		var found *string
		section := -1
		var m helpModule
		if len(parts) == 3 {
			var ok bool
			if m, ok = helpModuleByName(c, parts[0], role); ok {
				s, serr := strconv.Atoi(parts[1])
				n, nerr := strconv.Atoi(parts[2])
				if nerr == nil && n >= 0 && (parts[1] == "-" || serr == nil) {
					if serr == nil {
						section = s
					}
					if _, extra := helpParts(c, m, role, section); n < len(extra) {
						found = &extra[n].html
					}
				}
			}
		}
		if found == nil {
			c.Answer(c.T("misc.this_page_is_not_available"), true)
			return nil
		}
		c.Answer("", false)
		if c.Msg != nil {
			back := cbHelpModule + m.info.Name
			if section >= 0 {
				back = fmt.Sprintf("%s%s:%d", cbHelpSection, m.info.Name, section)
			}
			return c.Edit(*found, core.Keyboard([]gotgbot.InlineKeyboardButton{btn(c.T("common.back"), back)}))
		}
	case strings.HasPrefix(data, cbHelpPage):
		parts := strings.SplitN(data[len(cbHelpPage):], ":", 3)
		var texts []string
		number, part := -1, -1
		var m helpModule
		if len(parts) == 3 {
			var ok bool
			var e1, e2 error
			number, e1 = strconv.Atoi(parts[1])
			part, e2 = strconv.Atoi(parts[2])
			if m, ok = helpModuleByName(c, parts[0], role); ok && e1 == nil && e2 == nil && number >= 0 && part >= 0 {
				if pages := helpPages[m.info.Name]; number < len(pages) {
					texts = pages[number].Text(c)
				}
			}
		}
		if len(texts) == 0 || part >= len(texts) {
			c.Answer(c.T("misc.this_page_is_not_available"), true)
			return nil
		}
		c.Answer("", false)
		if c.Msg != nil {
			return c.Edit(texts[part], helpPageKeyboard(c, m, number, part, len(texts)))
		}
	}
	return nil
}
