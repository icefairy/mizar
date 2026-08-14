package plugins

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// HookCallback 是插件注册到某个挂载点的 JS 回调。
// ctxJSON 为挂载点上下文的 JSON 序列化（字段见 agent.HookContext）。
// 返回字符串会被记录（可做审计/日志）；返回 error 仅记录，不中断主流程。
type HookCallback func(ctxJSON string) (string, error)

type hookEntry struct {
	file string
	cb   HookCallback
}

// hookSnapshot 保存某个文件注册的全部回调，用于热重载失败时恢复。
type hookSnapshot struct {
	event string
	file  string
	cb    HookCallback
}

// hookRegistry 收集插件通过 hook_on 注册的回调。
type hookRegistry struct {
	mu    sync.RWMutex
	byEvt map[string][]hookEntry // event → entries（按注册顺序）
}

func newHookRegistry() *hookRegistry {
	return &hookRegistry{byEvt: make(map[string][]hookEntry)}
}

// Register 给事件追加一个回调（file 为来源插件文件名，用于热重载清理）。
func (r *hookRegistry) Register(file, event string, cb HookCallback) error {
	event = normalizeEvent(event)
	if event == "" {
		return fmt.Errorf("hook_on: 事件名不能为空")
	}
	if cb == nil {
		return fmt.Errorf("hook_on: 回调不能为 nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byEvt[event] = append(r.byEvt[event], hookEntry{file: file, cb: cb})
	return nil
}

// RemoveFile 移除某插件文件注册的全部回调（热重载/卸载时调用）。
func (r *hookRegistry) RemoveFile(file string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for evt, entries := range r.byEvt {
		kept := entries[:0]
		for _, e := range entries {
			if e.file != file {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(r.byEvt, evt)
		} else {
			r.byEvt[evt] = kept
		}
	}
}

// SnapshotFile 返回某文件注册的全部回调（热重载前保存，失败时恢复）。
func (r *hookRegistry) SnapshotFile(file string) []hookSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []hookSnapshot
	for evt, entries := range r.byEvt {
		for _, e := range entries {
			if e.file == file {
				out = append(out, hookSnapshot{event: evt, file: e.file, cb: e.cb})
			}
		}
	}
	return out
}

// RestoreFile 恢复快照中的回调（热重载失败时回滚）。
func (r *hookRegistry) RestoreFile(snaps []hookSnapshot) {
	if len(snaps) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range snaps {
		r.byEvt[s.event] = append(r.byEvt[s.event], hookEntry{file: s.file, cb: s.cb})
	}
}

// Fire 触发某事件：依次调用全部回调，返回 (成功数, 错误列表)。
// 回调内部异常被捕获为 error，不 panic。
func (r *hookRegistry) Fire(event string, ctxJSON string) (int, []error) {
	event = normalizeEvent(event)
	r.mu.RLock()
	cbs := make([]HookCallback, 0, 4)
	for _, e := range r.byEvt[event] {
		cbs = append(cbs, e.cb)
	}
	r.mu.RUnlock()

	ok := 0
	var errs []error
	for _, cb := range cbs {
		func() {
			// 单个回调 panic 不拖垮其他回调
			defer func() {
				if r := recover(); r != nil {
					errs = append(errs, fmt.Errorf("hook %s panic: %v", event, r))
				}
			}()
			if _, err := cb(ctxJSON); err != nil {
				errs = append(errs, fmt.Errorf("hook %s: %w", event, err))
			} else {
				ok++
			}
		}()
	}
	return ok, errs
}

// Count 返回事件已注册的回调数（测试/日志用）。
func (r *hookRegistry) Count(event string) int {
	event = normalizeEvent(event)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byEvt[event])
}

// normalizeEvent 事件名转小写并去掉连字符/下划线差异：
// "CompactionAfter" / "compaction-after" / "compaction_after" 归一为 "compactionafter"。
func normalizeEvent(event string) string {
	event = strings.ToLower(event)
	event = strings.ReplaceAll(event, "-", "")
	event = strings.ReplaceAll(event, "_", "")
	event = strings.TrimSpace(event)
	return event
}

// MarshalCtx 把任意结构序列化为 JSON 字符串（供 Fire 使用）。
func MarshalCtx(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
