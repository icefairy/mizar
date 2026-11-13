package server

import (
	"net/http"
	"strconv"
	"sync"
)

// ============================================================================
// 事件回放日志（Web UI 后台能力强化）
//
// 背景：手机/浏览器切到别的 App 后，页面被冻结、WS 连接被系统掐断（且 JS 收不到
// close），期间 Agent 仍在服务端继续跑、继续广播 streaming/tool 事件——但这些事件
// 此刻没有任何人接收，直接丢弃。用户切回来时看到的就是「中间过程全没了」。
//
// 方案：服务端为每个会话维护一份有界事件环形日志，并给每条会话事件分配单调递增
// 的 seq。前端记录自己「已经看到的最大 seq」，断线重连 / 回到前台时带上 after=seq
// 拉取缺失部分（GET /web/api/events），逐条重放后界面即与真实进度对齐。
//
// 为什么不上 Redis/DB：这是进程内短期缓冲（单实例 Agent、单一写者），落盘无意义——
// 进程重启后会话历史本来就已落盘（webstore），重放日志只需覆盖「同一次运行」。
// ============================================================================

// evtLogCap 每个会话保留的最大事件条数（约等于一次很长的任务的完整过程）。
// 超出后丢最旧的：极端长任务下宁可丢开头，也要保证最近的进度能重放回界面。
const evtLogCap = 4000

// evtLog 按会话保存事件序列（进程内、有界）。
type evtLog struct {
	mu  sync.Mutex
	seq map[string]int64
	buf map[string][]map[string]any
}

func newEvtLog() *evtLog {
	return &evtLog{seq: map[string]int64{}, buf: map[string][]map[string]any{}}
}

// append 记录一条会话事件并返回分配的 seq。
func (l *evtLog) append(conv string, ev map[string]any) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq[conv]++
	s := l.seq[conv]
	ev["seq"] = s
	buf := append(l.buf[conv], ev)
	if len(buf) > evtLogCap {
		buf = append([]map[string]any(nil), buf[len(buf)-evtLogCap:]...)
	}
	l.buf[conv] = buf
	return s
}

// since 返回 seq > after 的事件（after=0 表示全量）与当前最大 seq。
// 调用方拿到的事件里已带 seq 字段，前端据此推进游标。
func (l *evtLog) since(conv string, after int64) ([]map[string]any, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	last := l.seq[conv]
	buf := l.buf[conv]
	out := make([]map[string]any, 0, len(buf))
	for _, e := range buf {
		if s, ok := e["seq"].(int64); ok && s > after {
			out = append(out, e)
		}
	}
	return out, last
}

// nextSeq 当前会话的下一个 seq（=已用最大值）。前端可用来判断「是否漏了事件」。
func (l *evtLog) nextSeq(conv string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq[conv]
}

// reset 清空某会话的缓冲（新一轮运行开始时调用：旧的增量已并入落盘历史）。
// seq 保持单调递增，避免前端游标回退后重复重放。
func (l *evtLog) reset(conv string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buf, conv)
}

// emit 广播事件；会话相关事件先写入回放日志（带 seq）再广播。
// 所有需要「切后台/断线后补齐」的事件都必须走这里，而不是直接用 hub.broadcast。
func (s *Server) emit(ev map[string]any) {
	if s.evt != nil && ev["type"] == "event" {
		if conv, ok := ev["conversation_id"].(string); ok && conv != "" {
			s.evt.append(conv, ev)
		}
	}
	s.hub.broadcast(ev)
}

// handleWebEvents GET /web/api/events?conversation_id=<id>&after=<seq>
// 返回该会话 seq > after 的事件（断线/切后台期间缺失的部分），供前端重放。
// 同时返回 running/running_conv，让前端一次性判断「是否需要重新加载历史」。
func (s *Server) handleWebEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	conv := r.URL.Query().Get("conversation_id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if conv == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []any{}, "last": int64(0),
			"running": s.anyRunLive(), "running_conv": s.firstRunningConv(),
			"running_convs": s.runningConvsSnapshot(),
		})
		return
	}
	if s.evt == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []any{}, "last": int64(0), "after": after,
			"running": s.runLiveFor(conv), "running_conv": s.firstRunningConv(),
			"running_convs": s.runningConvsSnapshot(),
		})
		return
	}
	evs, last := s.evt.since(conv, after)
	if evs == nil {
		evs = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation_id": conv,
		"events":          evs,
		"last":            last,
		"after":           after,
		// 是否还有更早的、已被环形缓冲挤掉的事件（前端据此提示「过程已截断，
		// 请重新打开会话查看完整历史」而不是假装完整）。
		"truncated": (last - after) > int64(len(evs)),
		// running 按「这个会话」判断：多会话并发时别的会话在跑不代表本会话在跑。
		"running":       s.runLiveFor(conv),
		"running_conv":  s.firstRunningConv(),
		"running_convs": s.runningConvsSnapshot(),
	})
}
