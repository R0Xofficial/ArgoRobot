package core

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/i18n"
)

// The registry of modules.
//
// Every command, button and watcher belongs to the module that registered it. A module can be turned off for the
// whole bot (/module) or for one chat (/disablemodule); then none of its handlers run. A chat can also switch single
// commands (/disable) or all of a module's commands (/restrictmodule) off for its members.

// ModuleInfo is one module.
type ModuleInfo struct {
	Name        string
	Description string
	Protected   bool // can't be turned off for the whole bot
	English     bool // answers in English whatever language the chat speaks (the commands of the bot's staff)
	enabled     bool
}

// ModuleOption changes how a module is registered.
type ModuleOption func(*ModuleInfo)

// Protected modules can't be turned off with /module.
func Protected() ModuleOption { return func(m *ModuleInfo) { m.Protected = true } }

// EnglishOnly modules answer in English.
func EnglishOnly() ModuleOption { return func(m *ModuleInfo) { m.English = true } }

// ErrProtectedModule is returned when someone tries to turn off a protected module.
type protectedError struct{ Name string }

func (e *protectedError) Error() string { return e.Name + " is protected" }

// IsProtectedError tells whether err says a protected module can't be turned off.
func IsProtectedError(err error) bool { _, ok := err.(*protectedError); return ok }

// Never switched off by a chat: the switches themselves, so a chat can always undo what it did.
var protectedCommands = map[string]bool{
	"disable": true, "enable": true, "disabled": true, "restrictmodule": true, "unrestrictmodule": true,
	"disablemodule": true, "enablemodule": true,
}

// AlwaysOn is the only module a chat can't turn off for itself: it holds the switches.
var AlwaysOn = map[string]bool{"disabling": true}

type commandEntry struct{ primary, module string }

type registry struct {
	mu       sync.RWMutex
	modules  map[string]*ModuleInfo
	commands map[string]commandEntry // every name a command answers to -> its first name and module
}

func (r *registry) ensure() {
	if r.modules == nil {
		r.modules = map[string]*ModuleInfo{}
		r.commands = map[string]commandEntry{}
	}
}

// Module starts a module: what is registered next belongs to it.
func (r *Router) Module(name, description string, opts ...ModuleOption) {
	name = strings.ToLower(name)
	info := &ModuleInfo{Name: name, Description: description, enabled: true}
	for _, o := range opts {
		o(info)
	}
	reg := &r.app.registry
	reg.mu.Lock()
	reg.ensure()
	if old, ok := reg.modules[name]; ok { // a module registered in two parts keeps its first description
		info = old
	} else {
		reg.modules[name] = info
	}
	reg.mu.Unlock()
	r.current = name
}

func (a *App) recordCommand(names []string, module string) {
	if len(names) == 0 {
		return
	}
	primary := strings.ToLower(names[0])
	reg := &a.registry
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.ensure()
	for _, n := range names {
		n = strings.ToLower(n)
		if _, ok := reg.commands[n]; !ok {
			reg.commands[n] = commandEntry{primary, module}
		}
	}
}

// Modules lists the modules by name.
func (a *App) Modules() []ModuleInfo {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]ModuleInfo, 0, len(reg.modules))
	for _, m := range reg.modules {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Enabled is true when the module is on for the whole bot.
func (m ModuleInfo) Enabled() bool { return m.enabled }

// Module is one module by name, or nil.
func (a *App) Module(name string) *ModuleInfo {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if m, ok := reg.modules[strings.ToLower(name)]; ok {
		c := *m
		return &c
	}
	return nil
}

func (a *App) moduleEnabled(name string) bool {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	m, ok := reg.modules[name]
	return !ok || m.enabled
}

// LoadModuleState applies the on/off flags stored in the database.
func (a *App) LoadModuleState(ctx context.Context) error {
	rows, err := a.DB.Pool.Query(ctx, `SELECT name, enabled FROM modules`)
	if err != nil {
		return err
	}
	defer rows.Close()
	reg := &a.registry
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			return err
		}
		reg.mu.Lock()
		if m, ok := reg.modules[name]; ok && !m.Protected {
			m.enabled = enabled
			if !enabled {
				slog.Info("Module is disabled (stored state)", "module", name)
			}
		}
		reg.mu.Unlock()
	}
	return rows.Err()
}

// SetModuleEnabled turns a module on or off for the whole bot. found is false for an unknown module; turning a
// protected module off is an error that IsProtectedError recognises.
func (a *App) SetModuleEnabled(ctx context.Context, name string, enabled bool) (found bool, err error) {
	name = strings.ToLower(name)
	reg := &a.registry
	reg.mu.Lock()
	m, ok := reg.modules[name]
	if !ok {
		reg.mu.Unlock()
		return false, nil
	}
	if m.Protected && !enabled {
		reg.mu.Unlock()
		return true, &protectedError{name}
	}
	m.enabled = enabled
	reg.mu.Unlock()
	_, err = a.DB.Exec(ctx, `INSERT INTO modules (name, enabled) VALUES ($1, $2) ON CONFLICT (name) DO UPDATE SET enabled = EXCLUDED.enabled`, name, enabled)
	slog.Info("Module switched", "module", name, "enabled", enabled)
	return true, err
}

// CountModules is the number of loaded and of enabled modules.
func (a *App) CountModules() (loaded, enabled int) {
	for _, m := range a.Modules() {
		loaded++
		if m.enabled {
			enabled++
		}
	}
	return
}

// --- What a chat switched off ---

const (
	anonymousAdmin = 1087968824 // the user Telegram shows for anonymous admins
)

// CommandOf is (first name, module) of a command, or ok=false when no module has it.
func (a *App) CommandOf(name string) (primary, module string, ok bool) {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	e, found := reg.commands[strings.ToLower(strings.TrimLeft(name, "/!"))]
	return e.primary, e.module, found
}

// CommandsOf is the first names of a module's commands that can be switched off, sorted.
func (a *App) CommandsOf(module string) []string {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	seen := map[string]bool{}
	for _, e := range reg.commands {
		if e.module == strings.ToLower(module) && !protectedCommands[e.primary] {
			seen[e.primary] = true
		}
	}
	return sortedKeys(seen)
}

// AliasesOf is the other names of a command, sorted.
func (a *App) AliasesOf(primary string) []string {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	var out []string
	for n, e := range reg.commands {
		if e.primary == primary && n != primary {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Switched is everything a chat switched off: command names, "module:<name>" and "off:<name>".
func (a *App) Switched(ctx context.Context, chatID int64) (map[string]bool, error) {
	if cached, ok := a.switches.get(chatID); ok {
		return cached, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT command FROM disabled_commands WHERE chat_id = $1`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		found[k] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.switches.put(chatID, found)
	return found, nil
}

// SwitchOff switches something off in a chat. False when it already was.
func (a *App) SwitchOff(ctx context.Context, chatID int64, key string) (bool, error) {
	current, err := a.Switched(ctx, chatID)
	if err != nil || current[key] {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO disabled_commands (chat_id, command) VALUES ($1, $2) ON CONFLICT DO NOTHING`, chatID, key); err != nil {
		return false, err
	}
	next := copySet(current)
	next[key] = true
	a.switches.put(chatID, next)
	return true, nil
}

// SwitchOn switches something back on. False when it was not off.
func (a *App) SwitchOn(ctx context.Context, chatID int64, key string) (bool, error) {
	current, err := a.Switched(ctx, chatID)
	if err != nil || !current[key] {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, `DELETE FROM disabled_commands WHERE chat_id = $1 AND command = $2`, chatID, key); err != nil {
		return false, err
	}
	next := copySet(current)
	delete(next, key)
	a.switches.put(chatID, next)
	return true, nil
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func ModuleKey(module string) string { return "module:" + strings.ToLower(module) }
func OffKey(module string) string    { return "off:" + strings.ToLower(module) }

// ModuleOff tells whether the chat turned this whole module off. A broken lookup means "on".
func (a *App) ModuleOff(ctx context.Context, chatID int64, module string) bool {
	s, err := a.Switched(ctx, chatID)
	if err != nil {
		slog.Error("Looking up switched-off modules failed", "err", err)
		return false
	}
	return s[OffKey(module)]
}

// active is whether the module's handlers may run for this update: on for the bot and not off in the group.
func (c *Ctx) active(module string) bool {
	if module == "" {
		return true
	}
	if !c.App.moduleEnabled(module) {
		return false
	}
	if c.Msg != nil && isGroupType(c.Msg.Chat.Type) {
		return !c.App.ModuleOff(c.Ctx, c.Msg.Chat.Id, module)
	}
	if c.Update != nil && c.Update.ChatMember != nil && isGroupType(c.Update.ChatMember.Chat.Type) {
		return !c.App.ModuleOff(c.Ctx, c.Update.ChatMember.Chat.Id, module)
	}
	if c.Update != nil && c.Update.ChatJoinRequest != nil && isGroupType(c.Update.ChatJoinRequest.Chat.Type) {
		return !c.App.ModuleOff(c.Ctx, c.Update.ChatJoinRequest.Chat.Id, module)
	}
	return true
}

// blocked is whether this member's use of the command is switched off in the chat. Admins are never blocked.
func (c *Ctx) blocked(name, module string) bool {
	primary, _, ok := c.App.CommandOf(name)
	if !ok || protectedCommands[primary] {
		return false
	}
	off, err := c.App.Switched(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		slog.Error("Checking switched-off commands failed", "err", err)
		return false // never lock a chat out because of a bug
	}
	if !off[primary] && !off[ModuleKey(module)] {
		return false
	}
	if c.Msg.SenderChat != nil || (c.User != nil && c.User.Id == anonymousAdmin) {
		return false // anonymous admins and channels posting as the chat
	}
	if c.User == nil {
		return true
	}
	return !c.IsChatAdmin(c.Msg.Chat.Id, c.User.Id)
}

// moduleLang is the language a module answers in.
func (a *App) moduleEnglish(module string) bool {
	reg := &a.registry
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	m, ok := reg.modules[module]
	return ok && m.English
}

var _ = i18n.English

// IsProtectedCommand tells whether a chat may not switch the command off (the switches themselves).
func IsProtectedCommand(primary string) bool { return protectedCommands[primary] }

// IsGroup tells whether the chat is a group or a supergroup.
func IsGroup(chat gotgbot.Chat) bool { return isGroupType(chat.Type) }

// ModuleActive tells whether a module works in a chat: on for the bot and not switched off there.
func (a *App) ModuleActive(ctx context.Context, chatID int64, module string) bool {
	return a.moduleEnabled(module) && !a.ModuleOff(ctx, chatID, module)
}
