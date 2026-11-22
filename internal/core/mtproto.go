package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// MTProtoVersion names the MTProto library, its version and the API layer it speaks, e.g. "gotd/td v0.166.1 (layer 211)".
func MTProtoVersion() string {
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/gotd/td" {
				version = dep.Version
				if dep.Replace != nil && dep.Replace.Version != "" {
					version = dep.Replace.Version
				}
			}
		}
	}
	return fmt.Sprintf("gotd/td %s (layer %d)", version, tg.Layer)
}

// Full member lists through Telegram's MTProto interface (with gotd/td).
//
// The Bot API cannot list the members of a chat. MTProto can, for a bot that is an admin there, and that is what
// /zombies needs to find deleted accounts nobody has ever seen write. It is optional: without API_ID and API_HASH (from
// my.telegram.org) nothing here connects and ChatMembers says it has no list, and callers fall back to what the bot
// knows by itself.
//
// The client logs in with the bot token, only to read member lists; it receives no updates, so it never competes with
// the normal polling. Its session is kept in SessionFile (mount a volume there, or every start is a new login, which
// Telegram limits).

const (
	// SessionFile is where the MTProto session is kept. The first start logs in again.
	SessionFile      = "data/argo-mtproto.json"
	mtRetryAfter     = 5 * time.Minute // how long to wait before trying to connect again after a failure
	mtConnectTimeout = 60 * time.Second
	maxMembers       = 200_000
	participantsPage = 200
)

// ChatMember is somebody in a chat, as MTProto lists them.
type ChatMember struct {
	ID      int64
	Deleted bool
}

// MemberLister lists everyone in a chat. Replaceable in tests.
type MemberLister interface {
	Members(ctx context.Context, chatID int64) ([]ChatMember, error)
	Close() error
}

type mtState struct {
	connect  sync.Mutex // held while connecting, which can take a minute; mu is never held for that long
	mu       sync.Mutex
	lister   MemberLister
	failedAt time.Time
}

// MTProtoConfigured is true when API_ID and API_HASH are set (or a lister was given for tests).
func (a *App) MTProtoConfigured() bool {
	a.mt.mu.Lock()
	defer a.mt.mu.Unlock()
	return a.mt.lister != nil || (a.Cfg.APIID != 0 && a.Cfg.APIHash != "")
}

// SetMemberLister replaces the MTProto connection (for tests).
func (a *App) SetMemberLister(l MemberLister) {
	a.mt.mu.Lock()
	a.mt.lister = l
	a.mt.mu.Unlock()
}

func (a *App) memberLister() MemberLister {
	s := &a.mt
	ready := func() (MemberLister, bool) { // a lister, or whether to try connecting
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.lister != nil {
			return s.lister, false
		}
		try := a.Cfg.APIID != 0 && a.Cfg.APIHash != "" && (s.failedAt.IsZero() || time.Since(s.failedAt) >= mtRetryAfter)
		return nil, try
	}
	if l, try := ready(); l != nil || !try {
		return l
	}
	s.connect.Lock() // one connection attempt at a time
	defer s.connect.Unlock()
	if l, try := ready(); l != nil || !try {
		return l
	}
	c, err := connectMTProto(a.Cfg.APIID, a.Cfg.APIHash, a.Cfg.BotToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.failedAt = time.Now()
		slog.Warn("MTProto client could not connect: "+err.Error(), "module", "core.mtproto")
		return nil
	}
	s.lister = c
	slog.Info("MTProto client connected", "module", "core.mtproto")
	return c
}

// ChatMembers is everyone in the chat, or ok = false when that can't be had (MTProto is off or failed).
func (a *App) ChatMembers(ctx context.Context, chatID int64) ([]ChatMember, bool) {
	l := a.memberLister()
	if l == nil {
		return nil, false
	}
	list, err := l.Members(ctx, chatID)
	if err != nil {
		slog.Warn("MTProto member list failed", "chat", chatID, "err", err)
		return nil, false
	}
	return list, true
}

// StopMTProto closes the connection, if there is one.
func (a *App) StopMTProto() {
	a.mt.mu.Lock()
	l := a.mt.lister
	a.mt.lister = nil
	a.mt.mu.Unlock()
	if l != nil {
		if err := l.Close(); err != nil {
			slog.Warn("Closing the MTProto client failed", "err", err)
		}
	}
}

// --- The connection ---------------------------------------------------------------------

type mtClient struct {
	api    *tg.Client
	cancel context.CancelFunc
	done   chan struct{}
}

func connectMTProto(apiID int, apiHash, token string) (*mtClient, error) {
	if err := os.MkdirAll(filepath.Dir(SessionFile), 0o700); err != nil {
		return nil, err
	}
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: SessionFile},
		NoUpdates:      true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	mc := &mtClient{api: client.API(), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(mc.done)
		err := client.Run(ctx, func(ctx context.Context) error {
			st, err := client.Auth().Status(ctx)
			if err == nil && !st.Authorized {
				_, err = client.Auth().Bot(ctx, token)
			}
			ready <- err
			if err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		select {
		case ready <- err: // Run ended before it was ready
		default:
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			<-mc.done
			return nil, err
		}
		return mc, nil
	case <-time.After(mtConnectTimeout):
		cancel()
		return nil, errors.New("timed out")
	}
}

func (m *mtClient) Members(ctx context.Context, chatID int64) ([]ChatMember, error) {
	return listMembers(ctx, m.api, chatID, maxMembers)
}

func (m *mtClient) Close() error {
	m.cancel()
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

// mtAPI is the part of the MTProto API that listing members needs.
type mtAPI interface {
	ChannelsGetParticipants(ctx context.Context, request *tg.ChannelsGetParticipantsRequest) (tg.ChannelsChannelParticipantsClass, error)
	MessagesGetFullChat(ctx context.Context, chatID int64) (*tg.MessagesChatFull, error)
}

func usersOf(list []tg.UserClass, into map[int64]ChatMember, order *[]int64) {
	for _, uc := range list {
		u, ok := uc.(*tg.User)
		if !ok {
			continue
		}
		if _, dup := into[u.ID]; !dup {
			*order = append(*order, u.ID)
		}
		into[u.ID] = ChatMember{ID: u.ID, Deleted: u.Deleted}
	}
}

// listMembers is (id, deleted account) of everyone in the chat. A Bot API chat id -100... is a supergroup or channel
// (a bot may name a channel it belongs to without its access hash), other negative ids are basic groups.
func listMembers(ctx context.Context, api mtAPI, chatID int64, limit int) ([]ChatMember, error) {
	found := map[int64]ChatMember{}
	var order []int64
	switch {
	case chatID <= -1_000_000_000_000:
		channel := &tg.InputChannel{ChannelID: -chatID - 1_000_000_000_000}
		for offset := 0; offset < limit; {
			resp, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
				Channel: channel, Filter: &tg.ChannelParticipantsRecent{}, Offset: offset, Limit: participantsPage,
			})
			if err != nil {
				return nil, err
			}
			page, ok := resp.(*tg.ChannelsChannelParticipants)
			if !ok || len(page.Participants) == 0 {
				break
			}
			usersOf(page.Users, found, &order)
			offset += len(page.Participants)
			if offset >= page.Count {
				break
			}
		}
	case chatID < 0:
		full, err := api.MessagesGetFullChat(ctx, -chatID)
		if err != nil {
			return nil, err
		}
		usersOf(full.Users, found, &order)
	default:
		return nil, fmt.Errorf("chat %d is not a group", chatID)
	}
	out := make([]ChatMember, 0, len(order))
	for _, id := range order {
		out = append(out, found[id])
	}
	return out, nil
}
