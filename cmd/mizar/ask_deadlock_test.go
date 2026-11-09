package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"mizar/internal/builtins"
)

// 回归：用户回答 ask_user_question 后 TUI 彻底卡死。
//
// 根因：submitInput 整体运行在 tcell 事件循环线程（InputCapture → Enter → submitInput），
// 而回答分支用了 m.app.QueueUpdateDraw —— tview 的 QueueUpdate 是「投递后阻塞等待
// 回调在事件循环里执行完」，事件循环线程自己在等自己 → 自死锁，键盘全哑。
//
// 下面的静态断言把「submitInput 同步路径内不得出现 QueueUpdate/QueueUpdateDraw」
// 这个不变式锁住，防止以后又顺手写回去。tview 的自死锁无法在无终端单测里真实复现，
// 这层扫描是唯一能在 CI 里生效的防护。
//
// 只扫描同步路径：go func(){...} 里的调用跑在独立 goroutine，是正确用法（必须跳过）。
func TestSubmitInputNeverQueuesBlockingUpdate(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tui.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 tui.go 失败: %v", err)
	}
	var body *ast.BlockStmt
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "submitInput" {
			return true
		}
		body = fn.Body
		return false
	})
	if body == nil {
		t.Fatal("未找到 submitInput 函数")
	}
	var bad []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			return false // goroutine 内允许：不属事件循环线程
		case *ast.FuncLit:
			// 非 go 的直接回调仍会被执行，保守地继续深入检查
			return true
		case *ast.CallExpr:
			name := ""
			switch fun := node.Fun.(type) {
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			case *ast.Ident:
				name = fun.Name
			}
			if name == "QueueUpdateDraw" || name == "QueueUpdate" {
				bad = append(bad, name)
			}
		}
		return true
	})
	if len(bad) > 0 {
		t.Fatalf("submitInput 运行在事件循环线程，同步路径禁止调用 %v（会自死锁卡住整个 TUI）；"+
			"事件循环内请直接用 addChatLine/renderAllDirect，必要时开 goroutine", bad)
	}
}

// 单问题：一次输入即答完，payload 是含一个答案的 JSON 数组。
func TestApplyAnswerSingleQuestion(t *testing.T) {
	p := &askPending{qs: []builtins.Question{{
		ID:       "target",
		Question: "选哪个？",
		Options:  []builtins.Option{{Label: "最新 2.4.0"}, {Label: "维持 1.12.x"}},
	}}}
	payload, done := applyAnswer(p, "2")
	if !done {
		t.Fatal("单问题应一次就答完")
	}
	var got []builtins.Answer
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload 非法 JSON: %v (%s)", err, payload)
	}
	if len(got) != 1 || got[0].ID != "target" || got[0].Value != "维持 1.12.x" {
		t.Fatalf("答案解析错误: %+v", got)
	}
}

// 多问题：逐问推进，未答完不得发 payload。
func TestApplyAnswerMultiQuestionStaged(t *testing.T) {
	p := &askPending{qs: []builtins.Question{
		{ID: "a", Question: "第一问？", Options: []builtins.Option{{Label: "A1"}, {Label: "A2"}}},
		{ID: "b", Question: "第二问？", Options: []builtins.Option{{Label: "B1"}, {Label: "B2"}}},
	}}
	if payload, done := applyAnswer(p, "2"); done || payload != "" {
		t.Fatalf("第一问答完不应结束: done=%v payload=%s", done, payload)
	}
	if p.idx != 1 {
		t.Fatalf("idx 应推进到 1，实际 %d", p.idx)
	}
	payload, done := applyAnswer(p, "1")
	if !done {
		t.Fatal("第二问答完应结束")
	}
	var got []builtins.Answer
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload 非法 JSON: %v", err)
	}
	if len(got) != 2 || got[0].Value != "A2" || got[1].Value != "B1" {
		t.Fatalf("多问题答案顺序/内容错误: %+v", got)
	}
}

// 已答完（idx 越界）或 nil 时必须安全返回，不能 panic、不能重复发 payload。
func TestApplyAnswerAfterDoneIsNoop(t *testing.T) {
	if payload, done := applyAnswer(nil, "1"); done || payload != "" {
		t.Fatal("nil pending 应为空操作")
	}
	p := &askPending{qs: []builtins.Question{{ID: "a", Question: "问？"}}}
	applyAnswer(p, "x")
	if payload, done := applyAnswer(p, "y"); done || payload != "" {
		t.Fatalf("重复作答应为空操作: done=%v payload=%s", done, payload)
	}
}
