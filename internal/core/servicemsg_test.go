package core

import (
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func TestBackupGreetingServiceKinds(t *testing.T) {
	// A backup made before the kinds: the old switch meant join and leave.
	row := map[string]any{"clean_service": true}
	if err := backupGreeting(row); err != nil {
		t.Fatal(err)
	}
	if row["clean_service_types"] != "join,leave" || row["clean_service"] != false {
		t.Errorf("old switch: %v", row)
	}
	row = map[string]any{"clean_service": true, "clean_service_types": "pin"}
	if err := backupGreeting(row); err != nil || row["clean_service_types"] != "pin" || row["clean_service"] != false {
		t.Errorf("kinds win over the old switch: %v %v", row, err)
	}
	if err := backupGreeting(map[string]any{"clean_service_types": "join,bogus"}); err == nil {
		t.Error("an unknown kind is refused")
	}
}

func TestCleanSetAndDescribe(t *testing.T) {
	s := GreetSettings{CleanServiceTypes: "pin,bogus,photo"}
	if got := DescribeCleanKinds(s.CleanSet()); got != "photo, pin" {
		t.Errorf("describe: %q", got)
	}
	all := map[string]bool{}
	for _, k := range CleanKinds {
		all[k] = true
	}
	if DescribeCleanKinds(all) != "all" {
		t.Error("every kind is all")
	}
	if !(GreetSettings{CleanService: true}).Cleans("leave") || (GreetSettings{CleanService: true}).Cleans("pin") {
		t.Error("the old switch is join and leave")
	}
}

func TestServiceKind(t *testing.T) {
	cases := map[string]gotgbot.Message{
		"join":      {NewChatMembers: []gotgbot.User{{Id: 1}}},
		"leave":     {LeftChatMember: &gotgbot.User{Id: 1}},
		"photo":     {DeleteChatPhoto: true},
		"pin":       {PinnedMessage: &gotgbot.Message{}},
		"title":     {NewChatTitle: "x"},
		"videochat": {VideoChatEnded: &gotgbot.VideoChatEnded{}},
		"other":     {BoostAdded: &gotgbot.ChatBoostAdded{}},
		"":          {Text: "hi"},
	}
	for want, m := range cases {
		if got := ServiceKind(&m); got != want {
			t.Errorf("%q: got %q", want, got)
		}
	}
}
