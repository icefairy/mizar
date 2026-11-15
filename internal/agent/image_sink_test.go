package agent

import (
	"strings"
	"testing"
)

func resetImageState() {
	DrainPendingImages()
	SetImageVisionEnabled(false)
}

func TestImageMIME(t *testing.T) {
	cases := map[string]string{
		"a.PNG": "image/png", "a.jpg": "image/jpeg", "a.jpeg": "image/jpeg",
		"a.gif": "image/gif", "a.webp": "image/webp", "a.bmp": "image/bmp",
		"a.txt": "", "a.go": "",
	}
	for in, want := range cases {
		if got := ImageMIME(in); got != want {
			t.Errorf("ImageMIME(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPushDrainPendingImages(t *testing.T) {
	resetImageState()
	for i := 0; i < maxPending; i++ {
		if !PushPendingImage(PendingImage{Path: "x.png", MIME: "image/png"}) {
			t.Fatalf("第 %d 张应入队成功", i+1)
		}
	}
	if PushPendingImage(PendingImage{Path: "y.png"}) {
		t.Error("超过上限应拒绝")
	}
	got := DrainPendingImages()
	if len(got) != maxPending {
		t.Errorf("drain 数量=%d want %d", len(got), maxPending)
	}
	if len(DrainPendingImages()) != 0 {
		t.Error("drain 后队列应为空")
	}
}

func TestAttachPendingImages(t *testing.T) {
	resetImageState()
	PushPendingImage(PendingImage{Path: "a.png", MIME: "image/png", Base64: "AAA"})
	msgs := []Message{{Role: RoleUser, Content: "看图", Kind: KindNormal}}
	out, n := attachPendingImages(msgs)
	if n != 1 {
		t.Fatalf("挂载数=%d", n)
	}
	if len(out[0].Images) != 1 || out[0].Images[0].Path != "a.png" {
		t.Errorf("图片未挂到 user 消息: %+v", out[0].Images)
	}
}

func TestAttachPendingImagesSkipsToolResults(t *testing.T) {
	resetImageState()
	PushPendingImage(PendingImage{Path: "a.png", MIME: "image/png"})
	msgs := []Message{
		{Role: RoleUser, Content: "任务", Kind: KindNormal},
		{Role: RoleUser, Content: "工具结果: x", Kind: KindToolResult},
	}
	out, n := attachPendingImages(msgs)
	if n != 1 {
		t.Fatal("应挂载")
	}
	if len(out[0].Images) != 1 {
		t.Error("应挂到第一条普通 user 消息，而不是 tool result")
	}
	if len(out[1].Images) != 0 {
		t.Error("tool result 不应被挂图片")
	}
}

func TestAttachPendingImagesNoUserAppends(t *testing.T) {
	resetImageState()
	PushPendingImage(PendingImage{Path: "a.png", MIME: "image/png"})
	msgs := []Message{{Role: RoleAssistant, Content: "hi"}}
	out, n := attachPendingImages(msgs)
	if n != 1 || len(out) != 2 {
		t.Fatalf("应追加一条 user 消息: n=%d len=%d", n, len(out))
	}
	if !strings.Contains(out[1].Content, "图片") {
		t.Errorf("追加消息内容: %s", out[1].Content)
	}
}

func TestAttachPendingImagesEmpty(t *testing.T) {
	resetImageState()
	msgs := []Message{{Role: RoleUser, Content: "x"}}
	out, n := attachPendingImages(msgs)
	if n != 0 || len(out[0].Images) != 0 {
		t.Error("无待注入图片时不应改动")
	}
}
