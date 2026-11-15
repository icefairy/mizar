// 图片读取（P-7）：read 工具识别图片文件时，把图片作为附件回传，供多模态网关消费。
//
// 为什么用进程级 sink：read 工具在 builtins 包里，Agent 循环在 agent 包里，
// builtins 已经 import agent（反向不可行）。所以队列与类型住在这里，builtins
// 调用 PushPendingImage；Agent 每次 LLM 请求前 drain 并挂到最后一条 user 消息。
package agent

import (
	"path/filepath"
	"strings"
	"sync"
)

// PendingImage 待注入的图片附件。
type PendingImage struct {
	Path   string
	MIME   string
	Base64 string
}

var (
	pendingImgMu sync.Mutex
	pendingImgs  []PendingImage
	imageVision  bool
	maxPending   = 8
)

// SetImageVisionEnabled 设置是否启用图片读取（多模态网关可用时为 true）。
func SetImageVisionEnabled(on bool) {
	pendingImgMu.Lock()
	imageVision = on
	pendingImgMu.Unlock()
}

// ImageVisionEnabled 报告图片读取是否启用。
func ImageVisionEnabled() bool {
	pendingImgMu.Lock()
	defer pendingImgMu.Unlock()
	return imageVision
}

// PushPendingImage 入队一张图片（read 工具调用）。上限 8 张/轮。
func PushPendingImage(img PendingImage) bool {
	pendingImgMu.Lock()
	defer pendingImgMu.Unlock()
	if len(pendingImgs) >= maxPending {
		return false
	}
	pendingImgs = append(pendingImgs, img)
	return true
}

// DrainPendingImages 取出并清空待注入图片队列（Agent 循环每轮调用）。
func DrainPendingImages() []PendingImage {
	pendingImgMu.Lock()
	defer pendingImgMu.Unlock()
	if len(pendingImgs) == 0 {
		return nil
	}
	out := pendingImgs
	pendingImgs = nil
	return out
}

// ImageMIME 按扩展名返回 MIME（不是图片返回空串）。
func ImageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return ""
}

// MaxImageBytes 图片大小上限（超过不入队，避免 base64 撑爆请求）。
const MaxImageBytes = 8 * 1024 * 1024

// attachPendingImages 把待注入图片挂到最后一条 user 消息上（mutating）。
// 返回挂载的图片数量。无队列/无 user 消息时返回 0。
func attachPendingImages(msgs []Message) ([]Message, int) {
	pending := DrainPendingImages()
	if len(pending) == 0 {
		return msgs, 0
	}
	// 找最后一条可挂载的 user 消息（普通消息最合适；没有则新建）
	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser && msgs[i].Kind == KindNormal {
			idx = i
			break
		}
	}
	if idx < 0 {
		msgs = append(msgs, Message{Role: RoleUser, Content: "（图片附件）"})
		idx = len(msgs) - 1
	}
	for _, p := range pending {
		msgs[idx].Images = append(msgs[idx].Images, ImagePart{
			MIME: p.MIME,
			Data: p.Base64,
			Path: p.Path,
		})
	}
	return msgs, len(pending)
}
