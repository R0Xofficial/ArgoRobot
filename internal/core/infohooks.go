package core

// Extra flags and notes for /info (core/info_hooks.py). /info shows what the bot knows about everyone; modules add
// their own flags (Globally Banned, Whitelisted ...) by registering a provider. A provider returns (label, value)
// pairs always, so that /info can say "No" as well as "Yes". chatID is the chat /info was used in (0 in private
// chats); a flag that belongs to a chat is left out when there is none. A provider is skipped while its module is
// disabled.

// InfoFlag is one yes/no line.
type InfoFlag struct {
	Label string
	Value bool
}

type infoProvider struct {
	module string
	always bool
	fn     func(c *Ctx, userID, chatID int64) []InfoFlag
}

type infoNotes struct {
	module string
	fn     func(c *Ctx, userID, chatID int64) []string
}

var (
	infoProviders []infoProvider
	infoNoteList  []infoNotes
)

// RegisterInfo adds a provider of flags. Flags are normally left out for admins and the bot's staff; one registered
// with always is shown for everyone.
func RegisterInfo(module string, always bool, fn func(c *Ctx, userID, chatID int64) []InfoFlag) {
	p := infoProvider{module, always, fn}
	for i, old := range infoProviders {
		if old.module == module && old.always == always {
			infoProviders[i] = p // registering a module again (a new App in tests) replaces its provider
			return
		}
	}
	infoProviders = append(infoProviders, p)
}

// RegisterInfoNotes adds a provider of sentences (HTML) that /info shows at its end.
func RegisterInfoNotes(module string, fn func(c *Ctx, userID, chatID int64) []string) {
	for i, old := range infoNoteList {
		if old.module == module {
			infoNoteList[i].fn = fn
			return
		}
	}
	infoNoteList = append(infoNoteList, infoNotes{module, fn})
}

// InfoFlags are the flags of the providers registered with the same value of always.
func (c *Ctx) InfoFlags(userID, chatID int64, always bool) []InfoFlag {
	var out []InfoFlag
	for _, p := range infoProviders {
		if p.always == always && c.App.moduleEnabled(p.module) {
			out = append(out, p.fn(c, userID, chatID)...)
		}
	}
	return out
}

// InfoNotes are the sentences of every provider.
func (c *Ctx) InfoNotes(userID, chatID int64) []string {
	var out []string
	for _, p := range infoNoteList {
		if c.App.moduleEnabled(p.module) {
			out = append(out, p.fn(c, userID, chatID)...)
		}
	}
	return out
}
