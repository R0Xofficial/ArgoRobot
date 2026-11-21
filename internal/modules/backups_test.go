package modules_test

import (
	"encoding/json"
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func documentMsg(from Person, text, fileID string) Msg {
	return Msg{Chat: chat, From: from, Text: text, Extra: map[string]any{"document": map[string]any{"file_id": fileID, "file_unique_id": "u", "file_name": "b.json", "file_size": 100}}}
}

func TestBackupExportImport(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/export"})
	contains(t, lastText(t, e), "Only the owner of this chat")
	e.Send(Msg{Chat: chat, From: owner, Text: "/exportitems"})
	contains(t, lastText(t, e), "<code>approves</code>")
	e.Send(Msg{Chat: chat, From: owner, Text: "/export"})
	contains(t, lastText(t, e), "nothing to save")
	e.Send(Msg{Chat: chat, From: owner, Text: "/export bogus"})
	contains(t, lastText(t, e), `I don't know "bogus"`)

	e.Send(Msg{Chat: chat, From: alice, Text: "/save greet Hello *there*"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/filter hi Hi back"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve", Reply: &Msg{Chat: chat, From: dave, Text: "x"}})
	e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistname spam"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock sticker"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood 7"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: owner, Text: "/export"})
	contains(t, lastText(t, e), "The backup was sent to you")
	docs := e.Fake.Calls("sendDocument")
	if len(docs) != 1 || docs[0].P["chat_id"] != itoa(owner.ID) {
		t.Fatalf("documents: %+v", docs)
	}
	data := docs[0].P["_file_document"]
	var doc struct {
		Backup int `json:"argo_backup"`
		Items  map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(data), &doc); err != nil || doc.Backup != 1 {
		t.Fatalf("not a backup: %v\n%s", err, data)
	}
	for _, key := range []string{"notes", "filters", "approves", "blacklist", "joinblacklist", "locks", "antiflood"} {
		if key == "blacklist" || key == "joinblacklist" {
			continue
		}
		if _, ok := doc.Items[key]; !ok {
			t.Errorf("%s missing from the backup: %s", key, data)
		}
	}
	e.Send(Msg{Chat: chat, From: owner, Text: "/export"})
	contains(t, lastText(t, e), "Wait a little")

	// Wipe, then restore.
	for _, table := range []string{"notes", "filters", "approvals", "locks", "flood_settings"} {
		if _, err := e.App.DB.Exec(e.Ctx(), "DELETE FROM "+table+" WHERE chat_id = $1", chat); err != nil {
			t.Fatal(err)
		}
	}
	e.Fake.SetFile("bk1", []byte(data))
	e.Send(documentMsg(owner, "/import notes filters", "bk1"))
	got := lastText(t, e)
	contains(t, got, "Notes: 1 added")
	contains(t, got, "Filters: 1 added")
	if names, _ := e.App.FilterPatterns(e.Ctx(), chat); len(names) != 1 || names[0] != "hi" {
		t.Fatalf("filters not back: %v", names)
	}
	e.Send(documentMsg(owner, "/import", "bk1"))
	got = lastText(t, e)
	contains(t, got, "Notes: 0 added")
	contains(t, got, "1 already there")
	contains(t, got, "Approved users: 1 added")
	if ok, _ := e.App.IsApproved(e.Ctx(), chat, dave.ID); !ok {
		t.Fatal("dave is not approved again")
	}
	e.Send(documentMsg(owner, "/import notes overwrite", "bk1"))
	contains(t, lastText(t, e), "Notes: 1 written")

	// Garbage.
	e.Fake.SetFile("bad", []byte("not json"))
	e.Send(documentMsg(owner, "/import", "bad"))
	contains(t, lastText(t, e), "not a valid JSON")
	e.Fake.SetFile("other", []byte(`{"hello": 1}`))
	e.Send(documentMsg(owner, "/import", "other"))
	contains(t, lastText(t, e), "not a backup made by /export")
	e.Fake.SetFile("rows", []byte(`{"argo_backup":1,"items":{"notes":[{"name":"ok","content":"fine"},{"name":"bad name!","content":"x"},{"content":"no name"},"junk"],"warns":[{"user_id":-5,"created_at":"2026-01-01"}]}}`))
	e.Send(documentMsg(owner, "/import notes warns", "rows"))
	got = lastText(t, e)
	contains(t, got, "Notes: 1 added")
	if !strings.Contains(got, "skipped 3") && !strings.Contains(got, "3 skipped") && !strings.Contains(strings.ToLower(got), "skipped") {
		t.Fatalf("skipped rows not reported: %s", got)
	}
	e.Send(Msg{Chat: chat, From: owner, Text: "/import filters"})
	contains(t, lastText(t, e), "Reply to a backup file")
}
