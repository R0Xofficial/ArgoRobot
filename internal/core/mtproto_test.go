package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

type fakeMT struct {
	pages  [][]tg.UserClass // one page of users per call
	total  int
	calls  []int // the offsets asked for
	chatID int64
	err    error
}

func (f *fakeMT) ChannelsGetParticipants(_ context.Context, r *tg.ChannelsGetParticipantsRequest) (tg.ChannelsChannelParticipantsClass, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, r.Offset)
	f.chatID = r.Channel.(*tg.InputChannel).ChannelID
	i := len(f.calls) - 1
	if i >= len(f.pages) {
		return &tg.ChannelsChannelParticipantsNotModified{}, nil
	}
	parts := make([]tg.ChannelParticipantClass, len(f.pages[i]))
	for j := range parts {
		parts[j] = &tg.ChannelParticipant{}
	}
	return &tg.ChannelsChannelParticipants{Count: f.total, Participants: parts, Users: f.pages[i]}, nil
}

func (f *fakeMT) MessagesGetFullChat(_ context.Context, id int64) (*tg.MessagesChatFull, error) {
	f.chatID = id
	if f.err != nil {
		return nil, f.err
	}
	return &tg.MessagesChatFull{Users: f.pages[0]}, nil
}

func TestListMembersChannel(t *testing.T) {
	f := &fakeMT{total: 3, pages: [][]tg.UserClass{
		{&tg.User{ID: 1}, &tg.User{ID: 2, Deleted: true}},
		{&tg.User{ID: 3}, &tg.User{ID: 2, Deleted: true}, &tg.UserEmpty{ID: 9}},
	}}
	got, err := listMembers(context.Background(), f, -1001234567890, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if f.chatID != 1234567890 {
		t.Errorf("channel id %d", f.chatID)
	}
	if len(f.calls) != 2 || f.calls[0] != 0 || f.calls[1] != 2 {
		t.Errorf("offsets %v", f.calls)
	}
	want := []ChatMember{{1, false}, {2, true}, {3, false}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("%+v", got)
	}
}

func TestListMembersGroupAndErrors(t *testing.T) {
	f := &fakeMT{pages: [][]tg.UserClass{{&tg.User{ID: 5, Deleted: true}}}}
	got, err := listMembers(context.Background(), f, -4242, 1000)
	if err != nil || f.chatID != 4242 || len(got) != 1 || !got[0].Deleted {
		t.Fatalf("%+v %v chat %d", got, err, f.chatID)
	}
	if _, err := listMembers(context.Background(), &fakeMT{err: errors.New("boom")}, -1000000000001, 10); err == nil {
		t.Error("an error was swallowed")
	}
	if _, err := listMembers(context.Background(), f, 77, 10); err == nil {
		t.Error("a private chat id was accepted")
	}
	// The limit stops a huge chat.
	big := &fakeMT{total: 100000, pages: [][]tg.UserClass{{&tg.User{ID: 1}}, {&tg.User{ID: 2}}, {&tg.User{ID: 3}}}}
	if got, _ := listMembers(context.Background(), big, -1000000000001, 2); len(got) != 2 {
		t.Errorf("%d members", len(got))
	}
}

func TestMTProtoVersion(t *testing.T) {
	got := MTProtoVersion()
	t.Log(got)
	if !strings.HasPrefix(got, "gotd/td ") || !strings.Contains(got, " (layer ") {
		t.Errorf("version line: %q", got)
	}
}
