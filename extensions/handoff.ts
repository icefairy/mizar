// handoff.ts — 程序内快速生成交接文档（handoff.md）
//
// 用法:
//   /handoff 完成了登录模块; 下一步写支付接口    → 生成 handoff.md（备注段）
//   /handoff                                    → 无备注也生成
// 续接: 新会话首条消息 @handoff.md 继续
//
// 证据全部一手: git status / git log / git branch（shell_exec 采集）

export function command_handoff(args: string): string {
    const note = (args || "").trim();
    const now = new Date();
    const pad = function (n: number): string { return (n < 10 ? "0" : "") + n; };
    const ts = now.getFullYear() + "-" + pad(now.getMonth() + 1) + "-" + pad(now.getDate()) +
        " " + pad(now.getHours()) + ":" + pad(now.getMinutes()) + ":" + pad(now.getSeconds());

    // 一手证据采集（shell_exec 不可用时优雅降级）
    let branch = "";
    let status = "";
    let log = "";
    let gitOk = true;
    try {
        branch = shell_exec("git rev-parse --abbrev-ref HEAD").trim();
    } catch (e) {
        gitOk = false;
    }
    if (gitOk) {
        try { status = shell_exec("git status --short").trim(); } catch (e) { status = ""; }
        try { log = shell_exec("git log --oneline -10").trim(); } catch (e) { log = ""; }
    }

    const lines: string[] = [];
    lines.push("# Handoff");
    lines.push("");
    lines.push("- 时间: " + ts);
    if (gitOk) {
        lines.push("- 分支: `" + branch + "`");
    } else {
        lines.push("- 分支: (非 git 目录)");
    }
    lines.push("");
    if (gitOk) {
        lines.push("## 工作区状态 (git status --short)");
        lines.push("");
        lines.push(status || "(干净)");
        lines.push("");
        lines.push("## 最近提交 (git log -10)");
        lines.push("");
        lines.push(log || "(无)");
        lines.push("");
    }
    if (note) {
        lines.push("## 备注 / 下一步");
        lines.push("");
        lines.push(note);
        lines.push("");
    }
    lines.push("## 续接方式");
    lines.push("");
    lines.push("新会话首条消息: @handoff.md 继续");
    lines.push("");

    const md = lines.join("\n");
    try {
        fs_write("handoff.md", md);
    } catch (e) {
        return "handoff.md 写入失败: " + String(e);
    }
    return "已生成 handoff.md (" + md.length + " 字符)" + (note ? "，含备注" : "") +
        "。新会话: mizar 启动后首条消息输入 `@handoff.md 继续`";
}
