package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 把所有 server 包的测试隔离到临时 HOME。
//
// 背景（2026-11-14 实测）：webStorePath() 走 os.UserHomeDir()，测试里
// New(cfg, ...) 会创建真实的 ~/.mizar/webui.json 存储。多个测试
// （webui_compact_test / webui_steer_test / server_test …）会批量
// store.create / appendMsg，把「用户消息内容 / 只有一条 / /nosuchcmd」
// 这类测试数据直接写进用户的生产会话列表——用户看到的就是
// 「一堆没动过的历史会话时间显示成刚刚」。
//
// 之前只有 evtlog_test / webproviders_test 自己 t.Setenv("HOME")，
// 其余测试漏掉了。这里统一在包级兜住：无论哪个测试调 New()，
// HOME 都指向一次性临时目录，绝不碰真实 ~/.mizar。
//
// 需要自己控制 HOME 的测试（如 evtlog_test 的 t.Setenv）仍然有效——
// t.Setenv 的恢复值是 TestMain 设置的这个临时 HOME。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mizar-server-test-home-")
	if err != nil {
		panic("TestMain: 无法创建临时 HOME: " + err.Error())
	}
	oldHome, hadHome := os.LookupEnv("HOME")
	_ = os.Setenv("HOME", dir)

	code := m.Run()

	if hadHome {
		_ = os.Setenv("HOME", oldHome)
	} else {
		_ = os.Unsetenv("HOME")
	}
	_ = os.RemoveAll(dir)
	// 有些测试会往 $HOME/.mizar 下写东西，临时目录一并清掉。
	_ = os.RemoveAll(filepath.Join(dir, ".mizar"))
	os.Exit(code)
}
