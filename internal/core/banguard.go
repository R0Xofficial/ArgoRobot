package core

// Whether a global ban or a federation ban is about to remove a user from a chat.
//
// Kept in core so that the things that restrict or greet newcomers (captchas, antiraid, welcomes) can leave such a
// person alone: restricting someone who has just been banned would turn the ban into a mute, and nobody wants a
// welcome for somebody who is being removed.

// Spared tells whether federation bans leave this person alone in this chat: the bot itself, immune accounts, staff,
// people approved in the chat and its admins. The whitelist is not here: it protects from global bans only, and a
// federation ban is avoided by being approved in the chat.
func (c *Ctx) Spared(chatID, userID int64) bool {
	if userID == c.Bot.Id || IsImmune(userID) || c.App.GetRole(c.Ctx, userID) != NoRole {
		return true
	}
	if approved, _ := c.App.IsApproved(c.Ctx, chatID, userID); approved {
		return true
	}
	return c.IsChatAdmin(chatID, userID)
}

// GbanRemovable tells whether a globally banned user is to be removed from this chat right now (the chat may have
// /gbans off).
func (c *Ctx) GbanRemovable(chatID, userID int64) bool {
	if !c.App.Setting(c.Ctx, "gban_enforce", chatID) {
		return false
	}
	if protected, _ := c.App.IsProtected(c.Ctx, userID); protected {
		return false
	}
	return !c.Spared(chatID, userID)
}

// Gbanned: the user is globally banned and this chat removes such users.
func (c *Ctx) Gbanned(chatID, userID int64) bool {
	f := BanLookups.Gbanned
	if f == nil {
		return false
	}
	banned, err := f(c.Ctx, userID)
	return err == nil && banned && c.GbanRemovable(chatID, userID)
}

// Fbanned: the user is banned in this chat's federation (or one it subscribes to) and nobody spares them.
func (c *Ctx) Fbanned(chatID, userID int64) bool {
	f := BanLookups.ChatFedBan
	if f == nil {
		return false
	}
	banned, err := f(c.Ctx, chatID, userID)
	return err == nil && banned && !c.Spared(chatID, userID)
}

// BanApplies is either of the two: they are being removed from this chat.
func (c *Ctx) BanApplies(chatID, userID int64) bool {
	return c.Gbanned(chatID, userID) || c.Fbanned(chatID, userID)
}
