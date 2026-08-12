// 示例插件：磁盘监控工具（v0.1 测试用）
// Agent 自举的模板：导出 tool_ 前缀函数即可被自动注册。

/** 显示可用磁盘空间（模拟） */
export function tool_disk_watch(args: string): string {
    const target = args.trim() || "/";
    return `disk: ${target} 已用 42%, 剩余 58GB`;
}

/** 拼接问候语 */
export function tool_greet(args: string): string {
    return "你好, " + args + "! 我是开阳主星的辅星插件。";
}

/** 用宿主函数调 LLM */
export function tool_ask_llm(args: string): string {
    const reply = llm_chat(JSON.stringify([{role: "user", content: args}]));
    return "LLM说: " + reply;
}

/** 数学计算（无宿主依赖，纯 JS） */
export function tool_calc(args: string): string {
    // 简单安全表达式求值：仅支持 + - * / 和数字
    const tokens = args.split(/\s+/);
    if (tokens.length !== 3) return "用法: <a> <op> <b>";
    const a = parseFloat(tokens[0]);
    const op = tokens[1];
    const b = parseFloat(tokens[2]);
    let r = 0;
    switch (op) {
        case "+": r = a + b; break;
        case "-": r = a - b; break;
        case "*": r = a * b; break;
        case "/": r = b === 0 ? NaN : a / b; break;
        default: return "不支持的运算符: " + op;
    }
    return String(r);
}
