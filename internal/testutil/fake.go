// Package testutil is a fake Telegram Bot API server and a test environment around it. It lets the handlers
// run for real (router, middlewares, database) without Telegram: updates go in as JSON, and every call the bot
// makes is recorded.
package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Method string
	P      map[string]string
}

// Err is a Telegram error to return.
type Err struct {
	Code int
	Desc string
}

type Fake struct {
	Srv *httptest.Server

	mu      sync.Mutex
	calls   []Call
	members map[[2]int64]map[string]any // (chat, user) -> chat member object
	chats   map[string]map[string]any   // "123" or "@name" -> chat object
	fail    map[string][]Err            // method -> errors to return, in turn
	nextID  int
	updates []json.RawMessage // served once by getUpdates
}

func NewFake() *Fake {
	f := &Fake{members: map[[2]int64]map[string]any{}, chats: map[string]map[string]any{}, fail: map[string][]Err{}, nextID: 1000}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *Fake) Close() { f.Srv.Close() }

func userObj(id int64, name string) map[string]any {
	return map[string]any{"id": id, "is_bot": false, "first_name": name}
}

// SetMember makes a user a chat member with a status; extra holds admin rights ("can_restrict_members": true ...).
func (f *Fake) SetMember(chat, user int64, name, status string, extra map[string]any) {
	m := map[string]any{"status": status, "user": userObj(user, name)}
	for k, v := range extra {
		m[k] = v
	}
	if status == "administrator" {
		m["can_be_edited"] = false
		m["is_anonymous"] = false
	}
	if status == "creator" {
		m["is_anonymous"] = false
	}
	f.mu.Lock()
	f.members[[2]int64{chat, user}] = m
	f.mu.Unlock()
}

// SetChat makes getChat know a chat or user, by id ("123") and/or "@username".
func (f *Fake) SetChat(key string, obj map[string]any) {
	f.mu.Lock()
	f.chats[key] = obj
	f.mu.Unlock()
}

// Fail makes the next call of method fail with this error (queued in order).
func (f *Fake) Fail(method string, code int, desc string) {
	f.mu.Lock()
	f.fail[method] = append(f.fail[method], Err{code, desc})
	f.mu.Unlock()
}

// Calls are the recorded calls of one method (all methods when empty).
func (f *Fake) Calls(method string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// QueueUpdate makes the next getUpdates return this update (JSON).
func (f *Fake) QueueUpdate(raw string) {
	f.mu.Lock()
	f.updates = append(f.updates, json.RawMessage(raw))
	f.mu.Unlock()
}

// Reset forgets the recorded calls.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	// /bot<token>/<method>
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]
	_ = r.ParseMultipartForm(1 << 20)
	p := map[string]string{}
	for k, v := range r.MultipartForm.Value {
		p[k] = v[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, Call{method, p})
	var failure *Err
	if q := f.fail[method]; len(q) > 0 {
		failure, f.fail[method] = &q[0], q[1:]
	}
	f.mu.Unlock()

	reply := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v})
	}
	fail := func(code int, desc string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "description": desc})
	}
	if failure != nil {
		fail(failure.Code, failure.Desc)
		return
	}

	switch method {
	case "getUpdates":
		f.mu.Lock()
		out := f.updates
		f.updates = nil
		f.mu.Unlock()
		if len(out) == 0 {
			time.Sleep(300 * time.Millisecond) // a short "long poll"
			out = []json.RawMessage{}
		}
		reply(out)
	case "getMe":
		reply(map[string]any{"id": 999, "is_bot": true, "first_name": "Argo", "username": "ArgoTestBot"})
	case "getChatMember":
		var chat, user int64
		fmt.Sscan(p["chat_id"], &chat)
		fmt.Sscan(p["user_id"], &user)
		f.mu.Lock()
		m, ok := f.members[[2]int64{chat, user}]
		f.mu.Unlock()
		if !ok {
			fail(400, "Bad Request: user not found")
			return
		}
		reply(m)
	case "getChat":
		f.mu.Lock()
		c, ok := f.chats[p["chat_id"]]
		f.mu.Unlock()
		if !ok {
			fail(400, "Bad Request: chat not found")
			return
		}
		reply(c)
	case "sendMessage":
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		var chat int64
		fmt.Sscan(p["chat_id"], &chat)
		reply(map[string]any{"message_id": id, "date": 1, "chat": map[string]any{"id": chat, "type": "supergroup"}, "text": p["text"]})
	case "editMessageText":
		reply(map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": 1, "type": "supergroup"}, "text": p["text"]})
	default:
		reply(true)
	}
}
