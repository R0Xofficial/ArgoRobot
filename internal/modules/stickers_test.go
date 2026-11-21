package modules_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"argo/internal/core"

	. "argo/internal/testutil"
)

func testPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{200, 10, 10, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestStillPNG(t *testing.T) {
	out, err := core.StillPNG(testPNG(1000, 500), true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != 512 || cfg.Height != 256 {
		t.Errorf("scaled to %dx%d", cfg.Width, cfg.Height)
	}
	out, _ = core.StillPNG(testPNG(30, 20), false)
	cfg, _, _ = image.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != 30 || cfg.Height != 20 {
		t.Errorf("not kept: %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := core.StillPNG([]byte("junk"), true); err == nil {
		t.Error("junk was accepted")
	}
}

func stickerMsg(from Person, emoji string, extra map[string]any) Msg {
	st := map[string]any{"file_id": "stk", "file_unique_id": "uniq", "type": "regular", "width": 512, "height": 512,
		"is_animated": false, "is_video": false, "emoji": emoji}
	for k, v := range extra {
		st[k] = v
	}
	return Msg{Chat: chat, From: carol, Extra: map[string]any{"sticker": st}}
}

func TestGetSticker(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/getsticker"})
	contains(t, lastText(t, e), "sticker")
	e.Fake.SetFile("stk", testPNG(20, 20))
	m := stickerMsg(carol, "😀", nil)
	e.Send(Msg{Chat: chat, From: bob, Text: "/getsticker", Reply: &m})
	if len(e.Fake.Calls("sendDocument")) != 1 {
		t.Fatalf("%q", sent(e))
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/getsticker", Reply: &Msg{Chat: chat, From: carol, Extra: map[string]any{"sticker": map[string]any{"file_id": "broken", "file_unique_id": "u2", "type": "regular", "width": 1, "height": 1, "is_animated": false, "is_video": false}}}})
	if len(e.Fake.Calls("sendDocument")) != 0 {
		t.Error("a file that cannot be decoded was sent")
	}
}

func TestKang(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/kang"})
	contains(t, lastText(t, e), "sticker")

	// A new pack: the first one does not exist yet.
	e.Fake.Fail("getStickerSet", 400, "Bad Request: STICKERSET_INVALID")
	m := stickerMsg(carol, "😀", nil)
	e.Send(Msg{Chat: chat, From: bob, Text: "/kang 🔥", Reply: &m})
	calls := e.Fake.Calls("createNewStickerSet")
	if len(calls) != 1 {
		t.Fatalf("%q", sent(e))
	}
	contains(t, calls[0].P["stickers"], "🔥")
	contains(t, calls[0].P["name"], "s20_1_by_")
	contains(t, lastText(t, e), "🔥")

	// The cooldown, then an existing pack with a picture.
	e.Send(Msg{Chat: chat, From: bob, Text: "/kang", Reply: &m})
	contains(t, lastText(t, e), "Slow down")
	e.Fake.Reset()
	e.Fake.SetFile("pic", testPNG(800, 400))
	e.Send(Msg{Chat: chat, From: carol, Text: "/kang", Reply: &Msg{Chat: chat, From: bob, Photo: "pic"}})
	if len(e.Fake.Calls("addStickerToSet")) != 1 {
		t.Fatalf("%q", sent(e))
	}
	if strings.Contains(lastText(t, e), "{") {
		t.Errorf("%q", lastText(t, e))
	}

}
