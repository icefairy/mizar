#!/usr/bin/env python3
"""开阳(Mizar) 100 场景数据集 — 第二部分: dev37-dev70 + ops71-ops100
由 scenes.py 末尾 import 合并。"""

def extend(SCENES, S):
    # ---------- 测试 (8: dev37-dev44) ----------
    S(37, "dev", "task",       "为函数 add(a,b) 写一个 Go 单测,含一个正常+一个边界用例。", "含 TestAdd 与 t.Run", llm=True)
    S(38, "dev", "interactive", "pytest 里 fixture 的作用一句话+最小例子。", "含 @pytest.fixture", llm=True)
    S(39, "dev", "rpc",        "表驱动测试在 Go 的写法,给最小骨架。", "含 []struct 与 t.Run", llm=True)
    S(40, "dev", "task",       "jest 里 mock 一个模块的 API 是什么?", "含 jest.mock", llm=True)
    S(41, "dev", "task",       "TDD 红绿重构三步,各一句。", "红绿重构三步", llm=True)
    S(42, "dev", "interactive", "Go 里怎么跳过某个测试? 命令行参数是什么?", "含 -run 或 -short", llm=False)
    S(43, "dev", "rpc",        "覆盖率: go test 怎么输出覆盖率文件并查看?", "含 -coverprofile", llm=False)
    S(44, "dev", "task",       "断言两个 float 相等,为什么不能直接 ==, 应该怎么做?", "含 epsilon/误差", llm=True)

    # ---------- 文件读写/搜索 (8: dev45-dev52) ----------
    S(45, "dev", "task",       "读当前目录下 go.mod 的 module 行。", "read 工具或 grep 调用", workdir="/data/codes/mizar", llm=False)
    S(46, "dev", "task",       "在本仓库里搜 'compactor' 关键字出现的文件列表。", "grep/find 工具调用", workdir="/data/codes/mizar", llm=False)
    S(47, "dev", "task",       "列出 internal/ 下的一级子目录名。", "ls/find 工具调用", workdir="/data/codes/mizar", llm=False)
    S(48, "dev", "task",       "统计本仓库 .go 文件总数。", "find | wc 类命令", workdir="/data/codes/mizar", llm=False)
    S(49, "dev", "interactive", "查看 go.mod 的 module 名和 go 版本。", "含 module 行", workdir="/data/codes/mizar", llm=False)
    S(50, "dev", "task",       "找 internal/ 下最近修改的 .go 文件路径。", "find -t 或 ls -t", workdir="/data/codes/mizar", llm=False)
    S(51, "dev", "rpc",        "把 /tmp/mizar-scen-sandbox/hello.txt 内容改为 'hi mizar' (不存在则创建)。", "write/edit 工具调用", workdir="/tmp", llm=False)
    S(52, "dev", "task",       "在 /tmp/mizar-scen-sandbox 创建 a.txt 写入 'A', 再读取确认。", "write+read 链路", workdir="/tmp", llm=False)

    # ---------- git 操作 (6: dev53-dev58) ----------
    S(53, "dev", "task",       "git 查看最近5条提交记录的命令?", "含 git log -5", llm=False)
    S(54, "dev", "interactive", "git 当前分支名的命令?", "branch --show-current 或 rev-parse", llm=False)
    S(55, "dev", "rpc",        "git 暂存所有改动并提交,消息 'wip'。两条命令。", "git add -A 与 commit", llm=False)
    S(56, "dev", "task",       "git 撤销工作区某文件的未提交修改, 命令是?", "checkout -- 或 restore", llm=False)
    S(57, "dev", "task",       "git 查看某次提交改动的命令(带 hash 参数)?", "git show <hash>", llm=False)
    S(58, "dev", "interactive", "git 创建并切换新分支 feature-x 的两种方式?", "checkout -b / switch -c", llm=False)

    # ---------- LSP (6: dev59-dev64) ----------
    S(59, "dev", "task",       "LSP 能提供哪些代码智能能力? 列4种。", "补全/定义/引用/诊断", llm=True)
    S(60, "dev", "interactive", "gopls 是什么? 一句话。", "Go 官方 LSP server", llm=True)
    S(61, "dev", "rpc",        "LSP 的 textDocument/definition 用途一句话。", "跳转定义", llm=True)
    S(62, "dev", "task",       "LSP diagnostics 和编译器报错区别一句话。", "实时vs编译时", llm=True)
    S(63, "dev", "task",       "LSP hover 提供什么信息?", "类型/文档悬浮", llm=True)
    S(64, "dev", "interactive", "LSP references 有何用途?", "查引用位置", llm=True)

    # ---------- 技能 (3: dev65-dev67) ----------
    S(65, "dev", "task",       "Mizar 里技能(skill)的加载机制一句话描述。", "SKILL.md 按需加载", llm=True)
    S(66, "dev", "interactive", "skill_manage 工具能做什么?", "技能增删改沉淀", llm=True)
    S(67, "dev", "rpc",        "如何让 Mizar 在项目里自动应用 AGENTS.md? 一句话。", "workdir 下自动加载", llm=True)

    # ---------- 记忆 (3: dev68-dev70) ----------
    S(68, "dev", "task",       "mem_write 工具的用途一句话。", "写入记忆", llm=True)
    S(69, "dev", "interactive", "mem_search 和 mem_get 区别一句话。", "语义检索 vs 主键获取", llm=True)
    S(70, "dev", "rpc",        "记忆系统对 agent 的价值一句话。", "跨会话知识沉淀", llm=True)

    # ================== 运维域 (30: ops71-ops100) ==================
    S(71, "ops", "task",       "查看磁盘使用率命令?", "df -h", llm=False)
    S(72, "ops", "task",       "查看内存使用命令?", "free -h", llm=False)
    S(73, "ops", "interactive", "查看当前目录大小排序前5的文件命令?", "du -ah | sort | head", llm=False)
    S(74, "ops", "rpc",        "查看系统启动时长命令?", "uptime", llm=False)
    S(75, "ops", "task",       "批量重命名 /tmp/mizar-scen-sandbox/*.txt 为 .bak 的 bash 片段?", "for/mv 循环", llm=False)
    S(76, "ops", "task",       "tar 打包目录并 gzip 的命令?", "tar czf", llm=False)

    S(77, "ops", "task",       "tail -f 实时看日志, 如何只显示含 ERROR 的行?", "grep ERROR 或 tail|grep", llm=False)
    S(78, "ops", "interactive", "统计 access.log 中每个 IP 出现次数 top10 命令?", "awk sort uniq", llm=False)
    S(79, "ops", "rpc",        "journalctl 查看 nginx 服务最近100行的命令?", "journalctl -u -n", llm=False)
    S(80, "ops", "task",       "日志里 grep 时间段 [12:00-13:00] 的 sed/awk 思路一句话。", "时间正则匹配", llm=True)

    S(81, "ops", "task",       "ps 找到占用内存最高的进程命令?", "ps aux --sort=-%mem", llm=False)
    S(82, "ops", "interactive", "kill -9 和 kill -15 区别一句话。", "SIGKILL vs SIGTERM", llm=False)
    S(83, "ops", "rpc",        "nohup 与 & 后台运行的区别一句话。", "挂断信号忽略 vs 仅后台", llm=False)
    S(84, "ops", "task",       "top 里 load average 三个数含义一句话。", "1/5/15分钟负载", llm=True)

    S(85, "ops", "task",       "查看端口占用命令(lsof 或 ss)?", "lsof -i 或 ss -tlnp", llm=False)
    S(86, "ops", "interactive", "ping 和 curl 探活的差异一句话。", "ICMP vs HTTP层", llm=False)
    S(87, "ops", "rpc",        "nslookup/dig 用途一句话。", "DNS 解析查询", llm=False)

    S(88, "ops", "task",       "chmod 给脚本加可执行权限命令?", "chmod +x", llm=False)
    S(89, "ops", "task",       "chown 改文件属主命令格式?", "chown user:group file", llm=False)
    S(90, "ops", "interactive", "软链接与硬链接区别一句话。", "inode 层面差异", llm=True)
    S(91, "ops", "rpc",        "查找大文件(>100MB)的 find 命令?", "-size +100M", llm=False)

    S(92, "ops", "task",       "systemd 服务重启命令格式?", "systemctl restart xxx", llm=False)
    S(93, "ops", "interactive", "crontab -e 编辑后生效需要重启吗? 一句话。", "无需重启自动生效", llm=True)
    S(94, "ops", "rpc",        "环境变量 export 后为何子进程能继承? 一句话。", "fork 复制 environ", llm=True)

    S(95, "ops", "task",       "supervisorctl status 输出 RUNNING/FATAL 各代表什么?", "运行中/启动失败", llm=True)
    S(96, "ops", "interactive", "nginx reload 与 restart 区别一句话。", "热载配置 vs 全重启", llm=False)
    S(97, "ops", "rpc",        "健康检查端点一般返回什么状态码代表健康?", "200 ok", llm=True)

    S(98, "ops", "task",       "ssh 免密登录的公钥放目标机哪个文件?", "~/.ssh/authorized_keys", llm=False)
    S(99, "ops", "task",       "scp 拷贝本地文件到远程主机的命令格式?", "scp file user@host:path", llm=False)
    S(100,"ops","interactive", "ssh 端口转发本地转发 -L 的语法一句话?", "-L local:remote", llm=True)