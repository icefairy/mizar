// respond 工具：给模型的“直接回答”出口。
//
// 背景：mizar 的 agent 循环支持 tool_choice=required 强制工具调用，
// 但强制模式下模型需要一条“回答”的正规路径——否则会被迫硬调无关工具。
// respond 就是这条出口：模型想直接回答/汇报/宣布任务完成时调用它，
// agent 循环识别到后不执行工具、不回填结果，直接把 text 作为最终回答返回。
package builtins

import (
	"encoding/json"

	"mizar/internal/plugins"
)

// toolRespond 直接回答工具。参数 text 为最终回答内容（纯文本）。
// ArgsSchema 解析自 Description 的 "Args: {text: string}"，required 字段自动生成。
func toolRespond() plugins.Tool {
	return plugins.Tool{
		Name:        "respond",
		Description: "Answer the user directly with a final text response. Call this when you want to reply to the user, report progress/result, or announce that the task is done — instead of continuing to call other tools. Args: {text: string}",
		Run: func(args string) (string, error) {
			// 正常路径下 agent 循环会拦截 respond 直接返回，不会走到这里；
			// 走到这里说明被当作普通工具执行（如调试/手动调用），返回 text 本身。
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", err
			}
			return p.Text, nil
		},
	}
}
