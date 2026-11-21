package modules

import (
	"sort"
	"strings"

	"argo/internal/core"
	"argo/internal/text"
)

// Switching commands off (modules/disabling.py).

func registerDisabling(r *core.Router) {
	r.Command(func(c *core.Ctx) error { return switchCommand(c, true) }, "disable")
	r.Command(func(c *core.Ctx) error { return switchCommand(c, false) }, "enable")
	r.Command(func(c *core.Ctx) error { return switchMembers(c, true) }, "restrictmodule")
	r.Command(func(c *core.Ctx) error { return switchMembers(c, false) }, "unrestrictmodule")
	r.Command(func(c *core.Ctx) error { return switchModule(c, true) }, "disablemodule")
	r.Command(func(c *core.Ctx) error { return switchModule(c, false) }, "enablemodule")
	r.Command(cmdDisabled, "disabled")
}

func slash(name string) string { return "/" + name }

// beginSwitch is true when the person may change the chat's switches.
func beginSwitch(c *core.Ctx) bool {
	return c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo}) != nil
}

// firstWord is the first word after the command, without a leading / or !, in lower case.
func firstWord(c *core.Ctx) string {
	words := strings.Fields(c.Args)
	if len(words) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimLeft(words[0], "/!"))
}

func codes(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "<code>" + n + "</code>"
	}
	return strings.Join(out, ", ")
}

func slashes(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = slash(n)
	}
	return strings.Join(out, ", ")
}

// switchableModules are the modules a chat may turn off.
func switchableModules(c *core.Ctx) []string {
	var out []string
	for _, m := range c.App.Modules() {
		if !core.AlwaysOn[m.Name] {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

func switchCommand(c *core.Ctx, off bool) error {
	name := "enable"
	if off {
		name = "disable"
	}
	if !beginSwitch(c) {
		return nil
	}
	word := firstWord(c)
	if word == "" {
		c.Say(c.T("disabling.use_lt_command_gt_for_example", core.V{"name": name}))
		return nil
	}
	primary, _, ok := c.App.CommandOf(word)
	if !ok {
		c.Say(c.T("disabling.i_don_t_have_a_command", core.V{"slash": slash(text.Esc(word))}))
		return nil
	}
	if core.IsProtectedCommand(primary) {
		c.Say(c.T("disabling.can_t_be_switched_off", core.V{"slash": slash(primary)}))
		return nil
	}
	chatID := c.Msg.Chat.Id
	var changed bool
	var err error
	if off {
		changed, err = c.App.SwitchOff(c.Ctx, chatID, primary)
	} else {
		changed, err = c.App.SwitchOn(c.Ctx, chatID, primary)
	}
	if err != nil {
		return err
	}
	extra := ""
	if others := c.App.AliasesOf(primary); len(others) > 0 {
		extra = c.T("disabling.its_aliases_follow", core.V{"join": slashes(others)})
	}
	var msg string
	switch {
	case off && changed:
		msg = c.T("disabling.is_now_switched_off_for_members", core.V{"slash": slash(primary), "extra": extra})
	case off:
		msg = c.T("disabling.is_already_switched_off", core.V{"slash": slash(primary)})
	case changed:
		msg = c.T("disabling.is_switched_on_again", core.V{"slash": slash(primary), "extra": extra})
	default:
		msg = c.T("disabling.isn_t_switched_off", core.V{"slash": slash(primary)})
	}
	c.Say(msg)
	return nil
}

// membersModules are the modules with at least one command that can be switched off for members.
func membersModules(c *core.Ctx) []string {
	var out []string
	for _, m := range c.App.Modules() {
		if len(c.App.CommandsOf(m.Name)) > 0 {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

func switchMembers(c *core.Ctx, off bool) error {
	name := "unrestrictmodule"
	if off {
		name = "restrictmodule"
	}
	if !beginSwitch(c) {
		return nil
	}
	word := firstWord(c)
	mods := membersModules(c)
	listing := codes(mods)
	if word == "" {
		c.Say(c.T("disabling.use_lt_module_gt_modules", core.V{"name": name, "listing": listing}))
		return nil
	}
	if !contains(mods, word) {
		c.Say(c.T("disabling.i_don_t_have_a_module", core.V{"word": text.Esc(word), "listing": listing}))
		return nil
	}
	var changed bool
	var err error
	if off {
		changed, err = c.App.SwitchOff(c.Ctx, c.Msg.Chat.Id, core.ModuleKey(word))
	} else {
		changed, err = c.App.SwitchOn(c.Ctx, c.Msg.Chat.Id, core.ModuleKey(word))
	}
	if err != nil {
		return err
	}
	var msg string
	switch {
	case off && changed:
		msg = c.T("disabling.now_switched_off_for_members_admins")
	case off:
		msg = c.T("disabling.already_switched_off_for_members")
	case changed:
		msg = c.T("disabling.switched_on_again_for_members")
	default:
		msg = c.T("disabling.not_switched_off_for_members")
	}
	c.Say(c.T("disabling.the_commands_of_are", core.V{"word": word, "text": msg}))
	return nil
}

// endConnections ends the connections to a chat that turned them off; the people on them are told.
func endConnections(c *core.Ctx) (string, error) {
	people, err := c.App.DropChatConnections(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return "", err
	}
	title := c.Msg.Chat.Title
	if title == "" {
		title = text.Esc(c.Msg.Chat.Id)
	} else {
		title = text.Esc(title)
	}
	for _, id := range people {
		_ = c.SayTo(id, c.T("disabling.your_connection_to_has_ended_connections", core.V{"title": title}))
	}
	if len(people) == 0 {
		return "", nil
	}
	opt := "s"
	if len(people) == 1 {
		opt = ""
	}
	return c.T("disabling.connection_to_this_chat_ended", core.V{"people": len(people), "opt": opt}), nil
}

func switchModule(c *core.Ctx, off bool) error {
	name := "enablemodule"
	if off {
		name = "disablemodule"
	}
	if !c.RequireChatOwner() {
		return nil
	}
	word := firstWord(c)
	names := switchableModules(c)
	listing := codes(names)
	if word == "" {
		c.Say(c.T("disabling.use_lt_module_gt_or_all", core.V{"name": name, "listing": listing}))
		return nil
	}
	chatID := c.Msg.Chat.Id
	change := func(m string) (bool, error) {
		if off {
			return c.App.SwitchOff(c.Ctx, chatID, core.OffKey(m))
		}
		return c.App.SwitchOn(c.Ctx, chatID, core.OffKey(m))
	}

	if word == "all" {
		var changed []string
		for _, m := range names {
			ok, err := change(m)
			if err != nil {
				return err
			}
			if ok {
				changed = append(changed, m)
			}
		}
		switch {
		case !off && len(changed) > 0:
			c.Say(c.T("disabling.every_module_is_on_again"))
		case !off:
			c.Say(c.T("disabling.every_module_was_already_on"))
		case len(changed) > 0:
			ended := ""
			if contains(changed, "connection") {
				var err error
				if ended, err = endConnections(c); err != nil {
					return err
				}
			}
			c.Say(c.T("disabling.modules_are_now_off_in_this", core.V{"changed": len(changed)}) + ended)
		default:
			c.Say(c.T("disabling.everything_is_already_off_here"))
		}
		return nil
	}

	if !contains(names, word) {
		if c.App.Module(word) != nil {
			c.Say(c.T("disabling.is_part_of_my_core_and", core.V{"word": text.Esc(word)}))
		} else {
			c.Say(c.T("disabling.i_don_t_have_a_module", core.V{"word": text.Esc(word), "listing": listing}))
		}
		return nil
	}
	changed, err := change(word)
	if err != nil {
		return err
	}
	if off {
		if !changed {
			c.Say(c.T("disabling.is_already_off_here", core.V{"word": word}))
			return nil
		}
		ended := ""
		if word == "connection" {
			if ended, err = endConnections(c); err != nil {
				return err
			}
		}
		c.Say(c.T("disabling.is_now_off_in_this_chat", core.V{"word": word}) + ended)
		return nil
	}
	if changed {
		c.Say(c.T("disabling.is_on_again_in_this_chat", core.V{"word": word}))
	} else {
		c.Say(c.T("disabling.wasn_t_turned_off_here", core.V{"word": word}))
	}
	return nil
}

func cmdDisabled(c *core.Ctx) error {
	if !core.IsGroup(c.Msg.Chat) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	found, err := c.App.Switched(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	var off, members, commands []string
	for k := range found {
		switch {
		case strings.HasPrefix(k, "off:"):
			off = append(off, k[4:])
		case strings.HasPrefix(k, "module:"):
			members = append(members, k[7:])
		case !strings.Contains(k, ":"):
			commands = append(commands, k)
		}
	}
	if len(off)+len(members)+len(commands) == 0 {
		c.Say(c.T("disabling.nothing_is_switched_off_in_this"))
		return nil
	}
	sort.Strings(off)
	sort.Strings(members)
	sort.Strings(commands)
	var lines []string
	if len(commands) > 0 {
		lines = append(lines, c.T("disabling.commands_switched_off_for_members_admins")+slashes(commands))
	}
	if len(members) > 0 {
		lines = append(lines, c.T("disabling.modules_switched_off_for_members_admins")+codes(members))
	}
	if len(off) > 0 {
		lines = append(lines, c.T("disabling.modules_turned_off_for_everybody")+codes(off))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}
