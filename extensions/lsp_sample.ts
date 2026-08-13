// 示例插件：LSP Provider 注册
// 展示如何用 lsp_register_diagnostic / lsp_register_completion 注册自定义能力。
//
// 诊断提供者签名：lsp_register_diagnostic(name, fn(uri: string) => string)
//   返回 JSON 数组：[{startLine,startChar,endLine,endChar,severity,message,source}]
//
// 补全提供者签名：lsp_register_completion(name, fn(uri: string, line: number, col: number) => string)
//   返回 JSON 数组：[{label,kind,detail,insertText}]

// 以下声明是 Go 运行时注入的宿主函数（goja 自动适配），非编译期可用的 TS 类型。
declare const lsp_register_diagnostic: (
	name: string,
	fn: (uri: string) => string,
) => string | undefined;
declare const lsp_register_completion: (
	name: string,
	fn: (uri: string, line: number, col: number) => string,
) => string | undefined;
declare const fs_read: (path: string) => string;

/** 注册一个示例诊断提供者：检查 .go 文件中的 TODO 注释 */
function registerTodoDiag(): void {
	lsp_register_diagnostic("todo-checker", (uri: string): string => {
		// 读取文件内容
		const content = fs_read(uri);
		if (!content) return "[]";

		const lines = content.split("\n");
		const diags: any[] = [];

		for (let i = 0; i < lines.length; i++) {
			const line = lines[i];
			const idx = line.indexOf("TODO");
			if (idx >= 0) {
				diags.push({
					startLine: i,
					startChar: idx,
					endLine: i,
					endChar: idx + 4,
					severity: 3, // INFO
					message: "待办事项还未处理",
					source: "todo-checker",
				});
			}
			// 也检查 FIXME
			const fixIdx = line.indexOf("FIXME");
			if (fixIdx >= 0) {
				diags.push({
					startLine: i,
					startChar: fixIdx,
					endLine: i,
					endChar: fixIdx + 5,
					severity: 2, // WARN
					message: "需要修复的问题",
					source: "todo-checker",
				});
			}
		}

		return JSON.stringify(diags);
	});
}

/** 注册一个示例补全提供者：提供常用代码片段 */
function registerSnippetCompletion(): void {
	lsp_register_completion(
		"snippet-completer",
		(uri: string, _line: number, _col: number): string => {
			const items: any[] = [
				{
					label: "func main()",
					kind: 3,
					detail: "main 函数",
					insertText: "func main() {\n\t\n}\n",
				},
				{
					label: "if err != nil",
					kind: 14,
					detail: "错误检查",
					insertText: "if err != nil {\n\treturn err\n}\n",
				},
				{
					label: "for range",
					kind: 14,
					detail: "range 循环",
					insertText: "for _, v := range  {\n\t\n}\n",
				},
				{
					label: "type struct",
					kind: 7,
					detail: "结构体定义",
					insertText: "type  struct {\n\t\n}\n",
				},
			];

			// 只在 .go 文件中提供
			if (uri.endsWith(".go")) {
				return JSON.stringify(items);
			}
			return "[]";
		},
	);
}

// 插件加载时自动注册
registerTodoDiag();
registerSnippetCompletion();
