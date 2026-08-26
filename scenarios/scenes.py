#!/usr/bin/env python3
"""
开阳(Mizar) 100 场景数据集。开发70 + 运维30。
SCENES 列表每项: id, domain(dev|ops), mode(task|interactive|rpc), prompt, expect, workdir, timeout, llm, gold
llm=True 表示走真 LLM 推理(耗璇玑额度); False 表示走确定性的工具/文件操作(大部分无需 LLM,用于验证 Mizar 自身工具链)。
"""
# 工作目录: 用 mizar 自带的一个临时沙箱场景目录
SANDBOX = "/tmp/mizar-scen-sandbox"

SCENES = []

def S(sid, domain, mode, prompt, expect, workdir=None, timeout=200, llm=False, gold=None):
    SCENES.append(dict(id=sid, domain=domain, mode=mode, prompt=prompt,
                       expect=expect, workdir=workdir, timeout=timeout, llm=llm, gold=gold))

# ================== 开发域 (70) ==================

# ---------- 代码生成 (16: dev01-dev16) ----------
S(1,  "dev", "task",       "写一个 Go 函数: 判断整数 n 是否为质数。只要代码,带函数简短注释。", "返回含 func isPrime 的 Go 代码", llm=True)
S(2,  "dev", "task",       "生成冒泡排序的 Python 实现。只给 def bubble_sort。", "含 def bubble_sort 的代码", llm=True)
S(3,  "dev", "task",       "写一个返回斐波那契数列前N项的 TS 函数。",  "含含 fib 的函数", llm=True)
S(4,  "dev", "interactive", "给出一个 HTTP GET 请求的 Go 代码,用标准库 net/http, 打印响应状态码。", "含 http.Get 代码", llm=True)
S(5,  "dev", "task",       "写一个 JS 的节流 throttle 函数。", "含 throttle 函数", llm=True)
S(6,  "dev", "rpc",        "用 Python 写一个读取 CSV 并打印每行列数的脚本。", "含 csv 相关代码", llm=True)
S(7,  "dev", "task",       "SQL: 查订单表 orders 中每个客户 customer_id 的订单总数,按 SQL 给出。", "含 GROUP BY 的 SQL", llm=True)
S(8,  "dev", "task",       "写一个 Bash 脚本,遍历目录下所有 .log 文件,统计每个文件行数。", "含 for 循环+wc 的 bash", llm=True)
S(9,  "dev", "interactive", "生成一个 REST API 的 OpenAPI 3.0 描述片段,含 GET /users。", "含 /users 与 openapi", llm=True)
S(10, "dev", "task",       "写一个 Go 的并发 worker 池(sync.WaitGroup 会量),处理一个任务切片。", "含 WaitGroup 代码", llm=True)
S(11, "dev", "interactive", "实现一个 LRU 缓存的 Python 类,支持 put/get,容量上限。", "含 class LRU 与 get put", llm=True)
S(12, "dev", "task",       "写一个返回两个日期之间天数的 Python 函数。", "含 days_between 函数", llm=True)
S(13, "dev", "task",       "写一个 Shell 片段: 启动后台进程并 echo PID。", "含 & 与 $!", llm=False)
S(14, "dev", "rpc",        "给你一个目录写 .gitignore 内容,排除 node_modules 和 dist。", "含 node_modules 与 dist", llm=True)
S(15, "dev", "task",       "用 Go 写一个快速的 int 求平方根牛顿迭代函数。", "含 sqrt 相关代码", llm=True)
S(16, "dev", "interactive", "给一个 Markdown 表格示例(三列: 名称/类型/说明), 一行表头一行示例。", "含 | 列分隔表格", llm=True)

# ---------- 调试排障 (12: dev17-dev28) ----------
S(17, "dev", "task",       "以下 Go 代码为什么 panic 并修复: var s []int; fmt.Println(s[0])", "指出越界并给出修复", llm=True)
S(18, "dev", "interactive", "Python 报 'list index out of range', 常见三种导致原因是什么? 各给例子。", "至少两种原因", llm=True)
S(19, "dev", "rpc",        "解释 SQL 'SELECT * FROM t WHERE id = NULL' 为什么返回空, 怎么改。", "指出 IS NULL", llm=True)
S(20, "dev", "task",       "grep 定位: 在一个目录的 .go 文件里找含 TODO 的行,只输出文件:行号:内容。", "给出 grep -rn 命令", llm=False)
S(21, "dev", "task",       "bash 命令: 找出最近 10 分钟修改的 .java 文件。", "含 find -mmin 命令", llm=False)
S(22, "dev", "interactive", "git 提示 'Your branch is ahead', 怎么把本地提交推上去并创建远端分支? 给两条 git 命令。", "含 git push -u", llm=True)
S(23, "dev", "task",       "node.js 里 error-first callback 约定是什么? 一句话+示例签名。", "含 (err,data) 模式", llm=True)
S(24, "dev", "task",       "排查思路: 程序 100% CPU 卡住,Linux 上按什么顺序查? 列出前4步命令。", "含 top/ps/jstack 类", llm=True)
S(25, "dev", "interactive", "go build 报 'undefined: Foo', 最可能原因(给出三种)?", "三种原因", llm=True)
S(26, "dev", "rpc",        "chmod 755 和 644 分别代表什么权限? 一句话。", "含 owner 可读写执行", llm=True)
S(27, "dev", "task",       "curl 请求返回 502, 常见排查顺序? 给出3条命令。", "含 curl -I/netstat 类", llm=True)
S(28, "dev", "task",       "bash 记查看最近一次的 exit code 变量是哪个?", "含 $?", llm=False)

# ---------- 重构 (8: dev29-dev36) ----------
S(29, "dev", "task",       "将重复的 try/catch 抽成一个 helper 的速览: 给一个 JS wrapper 例子。", "含 function wrapper/try", llm=True)
S(30, "dev", "interactive", "简述 extract method 重构的步骤(3步)。", "含方法提取步骤", llm=True)
S(31, "dev", "rpc",        "把 if/else 链改成 switch 的注意点, 一句总结。", "提到 fallthrough/break", llm=True)
S(32, "dev", "task",       "变量命名建议: 避免缩写带来的问题,写一条经验。", "含明确命名建议", llm=True)
S(33, "dev", "task",       "如何把一个 Go struct 序列化时忽略空字段?", "含 omitempty", llm=True)
S(34, "dev", "interactive", "magic number 重构为 const 的意义,一句话。", "含 const 可维护性", llm=True)
S(35, "dev", "task",       "把两层 for 里实现相同的清理代码提取到函数, 给个结构示意。", "含提取函数结构", llm=True)
S(36, "dev", "rpc",        "检查命名: 给出 3 个易读反例并改成好名。", "反例+改法", llm=True)

# ---- 合并第二部分 (dev37-dev70 + ops71-ops100) ----
from scenes_ext import extend
extend(SCENES, S)

assert len(SCENES) == 100, f"expect 100 scenes, got {len(SCENES)}"