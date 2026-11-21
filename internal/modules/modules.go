// Package modules holds the commands of the bot, one file per module.
package modules

import (
	"argo/internal/core"
	"argo/internal/i18n"
)

// Register adds every ported module to the router.
func Register(app *core.App) {
	r := app.Router
	app.InstallBanLookups()
	r.Module("root", "Core bot commands", core.Protected(), core.EnglishOnly())
	registerRoot(r)
	r.Module("disabling", "Switching commands off")
	registerDisabling(r)
	r.Module("connection", "Managing a group from a private chat")
	registerConnection(r)
	r.Module("admin", "Group administration")
	registerAdmin(r)
	r.Module("promote", "Promote, demote, titles and tags")
	registerPromote(r)
	r.Module("purges", "Delete messages in bulk")
	registerPurges(r)
	r.Module("blacklists", "Forbidden words and phrases")
	registerBlacklists(r)
	r.Module("bans", "Ban commands for chat admins")
	registerBans(r)
	r.Module("mutes", "Mute commands for chat admins")
	registerMutes(r)
	r.Module("warns", "Warn commands for chat admins")
	registerWarns(r)
	registerWarnFilters(r)
	r.Module("antiflood", "Flood control")
	registerAntiflood(r)
	r.Module("locks", "Lock kinds of messages")
	registerLocks(r)
	r.Module("nightmode", "Nightmode: close the chat for the night")
	registerNightmode(r)
	r.Module("antiraid", "Antiraid: stop a flood of new accounts")
	registerAntiraid(r)
	r.Module("captcha", "Captchas for new members")
	registerCaptcha(r)
	r.Module("greetings", "Welcome and goodbye messages")
	registerGreetings(r)
	r.Module("gbans", "Global bans")
	registerGbans(r)
	r.Module("federations", "Federations: one ban list for many chats")
	registerFederations(r)
	r.Module("logchannel", "Log channel: a copy of a chat's moderation actions in a chat of the admins' choice")
	registerLogchannel(r)
	r.Module("misc", "Everyday commands")
	registerMisc(r)
	r.Module("zombies", "Zombies: remove deleted accounts from a chat")
	registerZombies(r)
	r.Module("backups", "Backups: save a chat's settings and data to a file, and put them back")
	registerBackups(r)
	r.Module("fun", "Lighter commands to break up moderation duty")
	registerFun(r)
	r.Module("stickers", "Sticker tools")
	registerStickers(r)
	r.Module("reporting", "Report messages to the admins")
	registerReporting(r)
	r.Module("afk", "Tell people you are away")
	registerAfk(r)
	r.Module("filters", "Saved replies to keywords")
	registerFilters(r)
	r.Module("joinblacklists", "Keep spammers out by their profile")
	registerJoinBlacklists(r)
	r.Module("approves", "Approved users: trusted people that automatic measures skip")
	registerApproves(r)
	r.Module("rules", "Chat rules")
	registerRules(r)
	r.Module("notes", "Saved messages anyone can ask for")
	registerNotes(r)
	r.Module("language", "Language of the bot's texts")
	registerLanguage(r)
}

// text detail for a log entry: both the label and the value are tags, written in the log chat's language.
func tagDetail(labelTag, valueTag string) core.Detail {
	return func(l i18n.Lang) (string, string) { return l.T(labelTag), l.T(valueTag) }
}

func valueDetail(labelTag, value string) core.Detail {
	return func(l i18n.Lang) (string, string) { return l.T(labelTag), value }
}
