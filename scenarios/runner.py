#!/usr/bin/env python3
"""
开阳(Mizar) 100 场景验证驱动 — 核心执行引擎
读 scenes.py 的 SCENES 列表，按模式执行并写结构化日志。
用法: /usr/bin/python3 runner.py [--scene ID] [--domain dev|ops] [--dry] [--batch N] [--llm]
"""
import json, os, subprocess, sys, time, argparse, re, threading, signal
from datetime import datetime

MIZAR_DIR = "/data/codes/mizar"
OUT_ROOT = os.path.join(MIZAR_DIR, "scenarios", "logs")
CUR_DIR = None  # 当前批次目录，main() 里设置，run_one 里写全量 stdout 用
# MIZAR 可执行：优先用已 build 的 ./mizar (快)，否则 go run
MIZAR_BIN = os.environ.get("MIZAR_BIN", os.path.join(MIZAR_DIR, "mizar"))
if not os.path.exists(MIZAR_BIN):
    MIZAR_BIN = None  # 回退 go run
USE_GO_RUN = MIZAR_BIN is None

RUN = 0
def new_run_id():
    global RUN
    RUN += 1
    return f"run-{datetime.now():%Y%m%d-%H%M%S}-{RUN}"

def stamp():
    return datetime.now().strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]

def now_ms():
    return int(time.time() * 1000)

# ---- 执行基元 ----
def run_task(prompt, timeout, cwd=None):
    """mode=task: 非交互单次执行。返回 (exit, stdout, stderr, ms)"""
    base = [MIZAR_BIN, "-task", prompt, "-tui=false"]
    if MIZAR_BIN is None:
        base = ["go", "run", "./cmd/mizar", "-task", prompt, "-tui=false"]
    t0 = now_ms()
    try:
        r = subprocess.run(base, cwd=cwd or MIZAR_DIR, capture_output=True,
                           text=True, timeout=timeout,
                           env={**os.environ, "NO_COLOR": "1"})
        return r.returncode, r.stdout, r.stderr, now_ms() - t0
    except subprocess.TimeoutExpired:
        return -9, "", "timeout>%ds" % timeout, now_ms() - t0

def run_interactive(prompt, timeout, cwd=None):
    """mode=interactive: 起 TUI(no-tui readline) 喂 stdin。逐行喂,等待退出。
    用 stdin 喂一行 + EOF 触发。"""
    base = [MIZAR_BIN, "-tui=false"]
    if MIZAR_BIN is None:
        base = ["go", "run", "./cmd/mizar", "-tui=false"]
    t0 = now_ms()
    try:
        r = subprocess.run(base, cwd=cwd or MIZAR_DIR, capture_output=True,
                           text=True, input=prompt + "\n",  # 读 stdin EOF 后自然退出
                           timeout=timeout, env={**os.environ, "PYCOLORS": "0"})
        return r.returncode, r.stdout, r.stderr, now_ms() - t0
    except subprocess.TimeoutExpired:
        return -9, "", "timeout", now_ms() - t0

def run_rpc(prompt, timeout, cwd=None, addr="127.0.0.1:39091"):
    """mode=rpc: 起 serve(独立端口) → JSON-RPC 调用 → 关闭。每次独立起服务隔离。"""
    import urllib.request, socket
    # 用非默认端口避免冲突
    t0 = now_ms()
    if MIZAR_BIN is None:
        base = ["go", "run", "./cmd/mizar", "-serve", "-addr", addr, "-token", "", "-tui=false"]
    else:
        base = [MIZAR_BIN, "-serve", "-addr", addr, "-token", "", "-tui=false"]
    proc = subprocess.Popen(base, cwd=cwd or MIZAR_DIR,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    ok = False
    for _ in range(40):
        try:
            import urllib.request
            req = urllib.request.Request(f"http://{addr}/v1/models")
            urllib.request.urlopen(req, timeout=2)
            ok = True
            break
        except Exception:
            if proc.poll() is not None:
                break
            time.sleep(0.25)
    if not ok:
        proc.terminate()
        return -1, "", "serve failed to start", now_ms() - t0
    # JSON-RPC 调用
    payload = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "agent.run",
        "params": {"task": prompt}
    }).encode()
    try:
        req = urllib.request.Request(f"http://{addr}/rpc", data=payload,
                                     headers={"Content-Type": "application/json"})
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = resp.read().decode()
    except Exception as e:
        proc.terminate()
        return -2, "", f"rpc error: {e}", now_ms() - t0
    finally:
        proc.terminate()
        try: proc.wait(timeout=3)
        except Exception: proc.kill()
    return 0, body, "", now_ms() - t0

# ---- 场景执行封装 ----
def run_one(sc, dry=False):
    """执行 single scene，返回记录 dict"""
    rec = {
        "id": sc["id"], "domain": sc["domain"], "mode": sc["mode"],
        "prompt": sc["prompt"], "expect": sc["expect"],
        "ts_start": datetime.now().isoformat(), "ts_end": None,
        "exit": None, "stdout_tail": "", "stderr_tail": "", "ms": None,
        "verdict": "not_run", "gold": sc.get("gold"),
        "llm": sc.get("llm", False),
    }
    if dry:
        rec["verdict"] = "dry"
        return rec
    try:
        to = sc.get("timeout", 120)
        if sc.get("llm"):
            to = max(to, 320)  # LLM agent 推理链可达 3m+，放宽避免误杀
        if sc["mode"] == "task":
            code, out, err, ms = run_task(sc["prompt"], to, sc.get("workdir"))
        elif sc["mode"] == "interactive":
            code, out, err, ms = run_interactive(sc["prompt"], to, sc.get("workdir"))
        elif sc["mode"] == "rpc":
            code, out, err, ms = run_rpc(sc["prompt"], to, sc.get("workdir"))
        else:
            return rec
    except Exception as e:
        return {**rec, "exit": -3, "stdout_tail": "", "stderr_tail": str(e), "ms": 0, "verdict": "error"}
    rec["exit"] = code
    rec["stdout_tail"] = out[-1500:]
    rec["stderr_tail"] = err[-800:]
    rec["ms"] = ms
    rec["pec_end"] = now_ms()
    rec["verdict"] = "pass" if code == 0 else "fail"
    # 全量 stdout 存独立文件，供严格复核(recheck)使用——tail 会截掉 interactive 的中段回答
    try:
        full_path = os.path.join(CUR_DIR or OUT_ROOT, f"scene-{sc['id']}.out.txt")
        with open(full_path, "w", encoding="utf-8", errors="replace") as f:
            f.write(out)
        rec["out_full_path"] = full_path
    except Exception:
        pass
    return rec

def rec_id(x):
    x["pec_end"] = now_ms(); x["verdict"] = "error"; return x

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scene", type=int, default=None)
    ap.add_argument("--domain", choices=["dev", "ops"], default=None)
    ap.add_argument("--mode", choices=["task", "interactive", "rpc"], default=None)
    ap.add_argument("--dry", action="store_true")
    ap.add_argument("--batch", type=int, default=1001)
    args = ap.parse_args()

    from scenes import SCENES
    sel = SCENES
    if args.scene is not None:
        sel = [s for s in sel if s["id"] == args.scene]
    if args.domain:
        sel = [s for s in sel if s["domain"] == args.domain]
    if args.mode:
        sel = [s for s in sel if s["mode"] == args.mode]

    tsdir = datetime.now().strftime("%Y%m%d-%H%M%S")
    outdir = os.path.join(OUT_ROOT, tsdir)
    os.makedirs(outdir, exist_ok=True)
    global CUR_DIR
    CUR_DIR = outdir

    results = []
    for n, sc in enumerate(sel, 1):
        print(f"[{n}/{len(sel)}] {sc['id']} {sc['domain']}/{sc['mode']}: {sc['prompt'][:60]}", flush=True)
        r = run_one(sc, args.dry)
        r["ts_end"] = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        results.append(r)
        fn = os.path.join(outdir, f"scene-{sc['id']}.json")
        with open(fn, "w") as f:
            json.dump(r, f, ensure_ascii=False, indent=2)
        # 节流: 真 LLM 场景每批间隔,避免上游 429
        if sc.get("llm"):
            time.sleep(1.2)
        if n % args.batch == 0:
            pass

    # 汇总
    summary = {
        "total": len(results),
        "pass": sum(1 for r in results if r["verdict"] == "pass"),
        "fail": sum(1 for r in results if r["verdict"] == "fail"),
        "err": sum(1 for r in results if r["verdict"] == "error"),
        "by_mode": {},
        "dir": outdir,
    }
    for r in results:
        m = summary["by_mode"].setdefault(r["mode"], {"pass":0,"fail":0,"err":0})
        m[r["verdict"]] = m.get(r["verdict"],0) + 1
    with open(os.path.join(outdir, "summary.json"), "w") as f:
        json.dump(summary, f, ensure_ascii=False, indent=2)
    print("\n=== SUMMARY ===", json.dumps(summary, ensure_ascii=False))
    # 失败清单
    fails = [r["id"] for r in results if r["verdict"] != "pass"]
    if fails:
        print("FAILS:", fails)

if __name__ == "__main__":
    main()