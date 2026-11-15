package engine

import "mizar/internal/redact"

// registerRedact 注册敏感信息脱敏宿主函数，供 TS/JS 插件使用：
//
//	redact_text(text)        → 脱敏后的文本（密钥/令牌/私钥/密码掩码）
//	redact_text(text, opts)  → opts 可传 {"phones": true} 额外脱敏手机号
//
// 为什么给插件这个能力：插件的工具结果会进模型上下文、也可能被写进文件/日志。
// 插件自己去 import agent.redact 是不可能的（跨语言 + 内部包），
// 所以必须由宿主暴露——对齐 hermes 的 ctx.redact_text()。
func registerRedact(reg func(name string, fn any)) {
	reg("redact_text", func(text string) string {
		return redact.Apply(text)
	})
	reg("redact_text_phone", func(text string) string {
		return redact.Text(text, redact.WithPhoneRedaction(true))
	})
}
