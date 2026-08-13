// Package utils 提供查询级的操作跟踪。
//
// 参考 openclaude 的 QueryLifecycleOperationTracker，在 Agent 循环中注入
// queryId/step/source 等结构化上下文，方便诊断和审计。
package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// QueryID 查询唯一标识。
type QueryID string

// GenerateQueryID 生成查询 ID。
func GenerateQueryID() QueryID {
	return QueryID(fmt.Sprintf("q-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&querySeq, 1)))
}

var querySeq uint64

// QueryLifecycle 单次查询的完整生命周期跟踪。
type QueryLifecycle struct {
	queryID     QueryID
	generation  int32
	startedAt   time.Time
	source      string
	step        int32
	mu          sync.Mutex
	activeOps   []activeOp
	completedAt time.Time
	terminalErr error
}

type activeOp struct {
	opType  string
	started time.Time
}

// QueryContext 查询上下文，传给 Agent 循环和挂载点。
type QueryContext struct {
	QueryID    QueryID
	Generation int
	Source     string
	Step       int
}

// NewQuery 创建新的查询生命周期跟踪。
func NewQuery(source string) *QueryLifecycle {
	return &QueryLifecycle{
		queryID:    GenerateQueryID(),
		source:     source,
		startedAt:  time.Now(),
		generation: 1,
	}
}

// QueryID 返回查询 ID。
func (q *QueryLifecycle) QueryID() QueryID {
	return q.queryID
}

// Context 返回查询上下文。
func (q *QueryLifecycle) Context() QueryContext {
	return QueryContext{
		QueryID:    q.queryID,
		Generation: int(q.generation),
		Source:     q.source,
		Step:       int(q.step),
	}
}

// BeginStep 进入下一步。
func (q *QueryLifecycle) BeginStep() {
	atomic.AddInt32(&q.step, 1)
}

// BeginOperation 标记一个操作开始（如 LLM 请求、工具调用）。
func (q *QueryLifecycle) BeginOperation(opType string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.activeOps = append(q.activeOps, activeOp{opType: opType, started: time.Now()})
}

// EndOperation 标记一个操作结束。
func (q *QueryLifecycle) EndOperation(opType string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := len(q.activeOps) - 1; i >= 0; i-- {
		if q.activeOps[i].opType == opType {
			q.activeOps = append(q.activeOps[:i], q.activeOps[i+1:]...)
			return
		}
	}
}

// Snapshot 返回当前操作快照。
func (q *QueryLifecycle) Snapshot() []activeOp {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]activeOp, len(q.activeOps))
	copy(out, q.activeOps)
	return out
}

// Complete 标记查询完成。
func (q *QueryLifecycle) Complete(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completedAt = time.Now()
	q.terminalErr = err
}

// FormatLog 生成结构化日志字符串。
func (q *QueryLifecycle) FormatLog(event string, extras ...string) string {
	ctx := q.Context()
	parts := []string{
		fmt.Sprintf("query.%s", event),
		fmt.Sprintf("queryId=%s", ctx.QueryID),
		fmt.Sprintf("generation=%d", ctx.Generation),
		fmt.Sprintf("source=%s", ctx.Source),
		fmt.Sprintf("step=%d", ctx.Step),
	}
	if ctx.Source != "" {
		parts = append(parts, fmt.Sprintf("source=%s", ctx.Source))
	}
	extraStr := strings.Join(extras, " ")
	if extraStr != "" {
		parts = append(parts, extraStr)
	}
	return strings.Join(parts, " ")
}

// Elapsed 返回查询运行时长。
func (q *QueryLifecycle) Elapsed() time.Duration {
	if !q.completedAt.IsZero() {
		return q.completedAt.Sub(q.startedAt)
	}
	return time.Since(q.startedAt)
}

// ContextWithQuery 将查询上下文注入 context.Context。
func ContextWithQuery(ctx context.Context, q *QueryLifecycle) context.Context {
	return context.WithValue(ctx, queryCtxKey{}, q)
}

type queryCtxKey struct{}

// QueryFromContext 从 context.Context 提取查询生命周期。
func QueryFromContext(ctx context.Context) *QueryLifecycle {
	if q, ok := ctx.Value(queryCtxKey{}).(*QueryLifecycle); ok {
		return q
	}
	return nil
}
