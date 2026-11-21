package core

import (
	"sync"
	"time"
)

// Telling a join or a leave that arrived as a service message from one that did not.
//
// Telegram announces who joined or left with a service message in the chat ("X joined the group"), and, to bots that
// are admins, with a chat_member update as well. In a group whose member list is hidden, or in a big one, the
// service messages are not sent at all, so only the update tells the bot. The greetings module handles both: the
// service message at once, and the update after a short wait, only when no service message came for the same person.

// MemberWait is how long an update waits for the service message of the same event.
var MemberWait = 3 * time.Second

const (
	memberWindow = 4 * time.Second // a service message this long before the update still counts as the same event
	memberMax    = 5000
)

type memberKey struct {
	kind         string
	chat, userID int64
}

type memberMarks struct {
	mu sync.Mutex
	m  map[memberKey]time.Time
}

// MarkMember records that a service message about the user ("join" or "leave") was handled.
func (a *App) MarkMember(kind string, chatID, userID int64) {
	a.marks.mu.Lock()
	defer a.marks.mu.Unlock()
	if a.marks.m == nil || len(a.marks.m) > memberMax {
		a.marks.m = map[memberKey]time.Time{}
	}
	a.marks.m[memberKey{kind, chatID, userID}] = time.Now()
}

// NoServiceMessage is for a chat_member update: it waits a moment and says whether the service message of the same
// event never came, so that the update has to do the greeting itself.
func (a *App) NoServiceMessage(kind string, chatID, userID int64) bool {
	arrived := time.Now()
	if MemberWait > 0 {
		time.Sleep(MemberWait)
	}
	a.marks.mu.Lock()
	defer a.marks.mu.Unlock()
	return !a.marks.m[memberKey{kind, chatID, userID}].After(arrived.Add(-memberWindow))
}
