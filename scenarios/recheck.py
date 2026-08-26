#!/usr/bin/env python3
"""复核 Mizar 100 场景: 判断"输出是否真的回答了问题"(非只看退出码)。
去掉横幅/时间戳噪音后, 检查输出是否含 expect 的核心线索(任一关键 token 命中即算通过)。
结论输出 pass/miss 与明细, 供人工复核真正 bug。
用法: /usr/bin/python3 recheck.py <logdir>
"""
import json, glob, os, re, sys

def norm(s):
    return re.sub(r"[\s,，。;；:：\-|/\\`'\"（）():：]+", "", s)

# 每个场景更宽松的关键token(取 expect 中 >=2 字的片段, 命中任一个即可)
def judge(d):
    expect = d.get("expect") or ""
    # 优先用全量 stdout 文件(避免 tail 截掉 interactive 中段的 AI 回答)
    out = ""
    fp = d.get("out_full_path")
    if fp and os.path.exists(fp):
        try:
            out = open(fp, encoding="utf-8", errors="replace").read()
        except Exception:
            out = d.get("stdout_tail", "") + d.get("stderr_tail", "")
    else:
        out = d.get("stdout_tail", "") + d.get("stderr_tail", "")
    # 去横幅噪音: logo 表格、时间戳、TUI 提示行、JSON-RPC 结构壳
    out = re.sub(r"(██+.*?\n|╠+.*?\n|═+.*?\n|◆.*?\n|v0\.[0-9].*?\n)", "", out)
    out = re.sub(r"^\s*[0-9]{4}/[0-9]{2}/[0-9]{2}.*?INFO.*?$", "", out, flags=re.M)
    out = re.sub(r"(已收到|已修正|已理解|明白|之后|收到，之后|之后所有|之后每次|之后将一律|之后将严格|之后将使用|之后将输出|之后回复|之后一律|之后所有回复)", "", out)
    out = re.sub(r"\{[^{}]*\"action\"\s*:\s*\"[^\"]*\"[^{}]*\}", "", out)  # JSON 结构壳
    out = re.sub(r"\{[^{}]*\"jsonrpc\"[^{}]*\}", "", out)
    out = re.sub(r"^\s*>+\s*$", "", out, flags=re.M)  # readline 提示符
    # 关键token候选: 从 expect 提取分词(长度>=2)
    toks = [t for t in re.split(r"[ ,/、\[\]\{\}()<>|\n]", expect) if len(norm(t)) >= 2]
    if not toks:
        return "hit", 0, 0  # 无可查token, 视为通过
    hit = 0
    for t in toks[:6]:
        nt = norm(t)
        if nt and nt in norm(out):
            hit += 1
    # 命中至少1个核心token即视为回答达标
    return ("hit" if hit >= 1 else "miss"), hit, len(toks)

def main():
    logdir = sys.argv[1] if len(sys.argv) > 1 else sorted(glob.glob("/data/codes/mizar/scenarios/logs/*/"), key=os.path.getmtime)[-1]
    files = sorted(glob.glob(os.path.join(logdir, "scene-*.json")))
    total=hit=miss=0
    misslist=[]
    for f in files:
        d=json.load(open(f))
        v,h,t=judge(d)
        total+=1
        if v=="hit": hit+=1
        else:
            miss+=1
            misslist.append((d["id"], d["mode"], d["expect"], (d["stdout_tail"] or "")[-200:].replace("\n"," ")))
    print(f"目录: {logdir}")
    print(f"回答达标: {hit}/{total}  miss: {miss}")
    print("\n--- MISS 明细 (需人工确认) ---")
    for sid,m,exp,out in misslist:
        print(f"  #{sid} {m} | 期望[{exp}] | 输出尾部: {out[:100]}")
    print(f"\n共 {total} 条")

if __name__=="__main__":
    main()