package core

import (
	"context"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// The service messages /cleanservice can delete, by kind.
var CleanKinds = []string{"join", "leave", "other", "photo", "pin", "title", "videochat"}

// ServiceKind says which kind of service message this is ("join", "pin"...), or "" for an ordinary message.
func ServiceKind(m *gotgbot.Message) string {
	switch {
	case len(m.NewChatMembers) > 0:
		return "join"
	case m.LeftChatMember != nil:
		return "leave"
	case len(m.NewChatPhoto) > 0 || m.DeleteChatPhoto || m.ChatBackgroundSet != nil:
		return "photo"
	case m.PinnedMessage != nil:
		return "pin"
	case m.NewChatTitle != "" || m.ForumTopicCreated != nil || m.ForumTopicEdited != nil:
		return "title"
	case m.VideoChatScheduled != nil || m.VideoChatStarted != nil || m.VideoChatEnded != nil || m.VideoChatParticipantsInvited != nil:
		return "videochat"
	case m.MessageAutoDeleteTimerChanged != nil || m.BoostAdded != nil || m.SuccessfulPayment != nil ||
		m.ProximityAlertTriggered != nil || m.WebAppData != nil || m.ForumTopicClosed != nil || m.ForumTopicReopened != nil ||
		m.GeneralForumTopicHidden != nil || m.GeneralForumTopicUnhidden != nil || m.ChecklistTasksDone != nil ||
		m.ChecklistTasksAdded != nil || m.GiveawayCreated != nil || m.GiveawayCompleted != nil ||
		m.WriteAccessAllowed != nil || m.PaidMessagePriceChanged != nil || m.UsersShared != nil || m.ChatShared != nil:
		return "other"
	}
	return ""
}

// CleanSet is the kinds of service messages the chat has deleted. The old on/off switch meant join and leave.
func (s GreetSettings) CleanSet() map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Split(s.CleanServiceTypes, ",") {
		for _, kind := range CleanKinds {
			if word == kind {
				set[kind] = true
			}
		}
	}
	if len(set) == 0 && s.CleanService {
		set["join"], set["leave"] = true, true
	}
	return set
}

// Cleans says whether the chat has this kind of service message deleted.
func (s GreetSettings) Cleans(kind string) bool { return s.CleanSet()[kind] }

// DescribeCleanKinds writes the kinds as the admins write them: "all", or a list in a fixed order.
func DescribeCleanKinds(set map[string]bool) string {
	var names []string
	for _, kind := range CleanKinds {
		if set[kind] {
			names = append(names, kind)
		}
	}
	if len(names) == len(CleanKinds) {
		return "all"
	}
	return strings.Join(names, ", ")
}

// SetCleanKinds saves the kinds; the old flag is cleared so that it can never bring join and leave back.
func (a *App) SetCleanKinds(ctx context.Context, chatID int64, set map[string]bool) error {
	var names []string
	for _, kind := range CleanKinds {
		if set[kind] {
			names = append(names, kind)
		}
	}
	return a.UpdateGreetings(ctx, chatID, map[string]any{"clean_service_types": strings.Join(names, ","), "clean_service": false})
}

// cleanService deletes the service message once everything else has dealt with it, when the chat chose
// its kind with /cleanservice. A welcome or a ban still sees the message it answers.
func (r *Router) cleanService(c *Ctx) {
	m := c.Msg
	if m == nil || c.ServiceGone || !isGroupType(m.Chat.Type) {
		return
	}
	kind := ServiceKind(m)
	if kind == "" || !c.active("greetings") {
		return
	}
	s, err := r.app.Greetings(c.Ctx, m.Chat.Id)
	if err != nil || !s.Cleans(kind) {
		return
	}
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
}
