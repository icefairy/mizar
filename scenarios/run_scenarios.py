#!/usr/bin/env python3
"""
开阳(Mizar) 100 场景验证驱动 —— 交互/非交互/RPC 三种模式
用法:  <python> run_scenarios.py [--scene N] [--domain dev|ops] [--dry]
产出:  logs/<ts>/ 下每条 scene-<id>.json (时间戳/模式/输入/输出摘要/退出码/耗时/预期与实际)
针对含 LLM 推理的场景, 谨慎控制并行度避免打爆璇玑上游限额(429)。
"""
import json, os, subprocess, sys, time, re, argparse, shutil, select, threading
from datetime import datetime

MIZAR = os.environ.get("MIZAR_BIN", "go")  # "go run ./cmd/mizar"  或构建好的 ./mizar
MIZAR_DIR = "/data/codes/mizar"
OUT_ROOT = "/data/codes/mizar/scenarios/logs"
BA5E = "/usr/bin/python3"

# ---------------- 场景定义 ----------------
# mode: task=非交互一次；interactive=喂stdin；rpc=经serve
# 输入格式/预期: 文本描述 (供人工/机器判)
# 域: dev / ops
SCENES = []
def S(sid, domain, mode, prompt, expect, workdir=None, timeout=120, gold=None):
    SCEN.append(dict(id=sid, domain=domain, mode=mode, prompt=prompt,
                     expect=expect, workdir=workdir, timeout=timeout, gold=gold))

# ============ 开发域 开发70 ============
# --- A. 代码生成 (16) ---
add=...