package jobs

import (
	"strings"
	"testing"
	"time"
)

// 后台任务里的 panic 必须被兜住：否则整个 mizar 进程退出。
func TestJobPanicDoesNotKillProcess(t *testing.T) {
	r := NewRegistry()
	id := r.Start("test", "会 panic 的任务", func() (string, error) {
		panic("后台任务炸了")
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, st, ok := r.Output(id)
		if ok && st != StatusRunning {
			if st != StatusErrored {
				t.Fatalf("panic 任务的状态应为 errored，实际 %v", st)
			}
			// 错误信息通过 List 取
			for _, j := range r.List() {
				if j.ID == id {
					if !strings.Contains(j.Error, "panicked") {
						t.Fatalf("错误信息应说明 panic: %q", j.Error)
					}
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("panic 任务 5s 内未结束")
}

// 正常任务不受影响。
func TestJobNormalStillWorks(t *testing.T) {
	r := NewRegistry()
	id := r.Start("test", "正常任务", func() (string, error) {
		return "hello", nil
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, st, ok := r.Output(id)
		if ok && st == StatusDone {
			if out != "hello" {
				t.Fatalf("输出应为 hello，实际 %q", out)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("正常任务 5s 内未完成")
}
