#!/usr/bin/env python3
"""与 pi 的对比双跑：把 12 条典型重叠场景分别跑 mizar(task) 和 pi(-p 非交互)，
记录双方输出/耗时/退出码到 JSON，供“不如 pi 的地方”定位。
用法: /usr/bin/python3 pi_compare.py

说明: pi 可执行 = pi (node 包), 非交互 -p 模式。真实 LLM 消耗, 12 条约 2-6 分钟。
"""
import json, subprocess, time, os, sys

def now_ms():
    return int(time.time() * 1000)

# 12 条重叠场景 (mizar scenes 的 id 复用, prompt 相同)
PAIRS = [
    (1,  "写一个 Go 函数判断整数 n 是否为质数, 只要代码带简短注释"),
    (2,  "生成冒泡排序的 Python 实现, 只给 def bubble_sort"),
    (7,  "SQL: 查订单表 orders 中每个客户 customer_id 的订单总数"),
    (15, "给一个 Go 平方根函数代码(用 math.Sqrt)"),
    (17, "指出这段 Go 代码 panic 的可能原因: var s []int; s[0]=1 并给修复"),
    (27, "Nginx 返回 502 表示什么? 排查思路一句话"),
    (41, "TDD 的红绿重构三步是什么, 一句话"),
    (56, "git 撤销工作区某文件的未提交修改的命令是?"),
    (62, "LSP diagnostics 和编译器报错区别一句话"),
    (71, "查看磁盘使用率的命令?"),
    (77, "tail -f 实时看日志只显示含 ERROR 行的方法?"),
    (85, "查看端口占用命令(lsof 或 ss)?"),
]

def run_mizar(prompt):
    t0 = now_ms()
    try:
        r = subprocess.run(["/data/codes/mizar/mizar", "-task", prompt, "-tui=false"],
                           capture_output=True, text=True, cwd="/data/codes/mizar",
                           timeout=320, env={**os.environ, "PYCOLORS": "0"})
        return {"exit": r.returncode, "out": r.stdout[-1200:], "ms": now_ms()-t0}
    except subprocess.TimeoutExpired:
        return {"exit": "TIMEOUT", "out": "", "ms": now_ms()-t0}

def run_pi(prompt):
    t0 = now_ms()
    try:
        r = subprocess.run(["pi", "-p", "--append-system-prompt",
                            "直接回答问题,简洁中文。", prompt],
                           capture_output=True, text=True, timeout=320,
                           env={**os.environ, "PYCOLORS": "0"})
        return {"exit": r.returncode, "out": (r.stdout or r.stderr)[-1200:], "ms": now_ms()-t0}
    except subprocess.TimeoutExpired:
        return {"exit": "TIMEOUT", "out": "", "ms": now_ms()-t0}

def main():
    outdir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "logs")
    os.makedirs(outdir, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S")
    results = []
    for id, prompt in PAIRS:
        print(f"[{id}] mizar vs pi: {prompt[:40]}...", flush=True)
        m = run_mizar(prompt)
        p = run_pi(prompt)
        results.append({"scene": id, "prompt": prompt, "mizar": m, "pi": p})
        print(f"    mizar exit={m['exit']} {m['ms']}ms | pi exit={p['exit']} {p['ms']}ms", flush=True)
    path = os.path.join(outdir, f"pi-compare-{ts}.json")
    json.dump(results, open(path, "w"), ensure_ascii=False, indent=2)
    print("written:", path)

if __name__ == "__main__":
    main()