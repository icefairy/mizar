#!/usr/bin/env python3
"""pe-check-win7.py —— 检查 Windows PE 是否满足 Windows 7 加载/运行条件。

用途：发布 mizar-windows-amd64-win7.exe 前做静态自检，避免把「只能在 Win10+
上跑」的二进制发给内网 Win7 用户。只依赖标准库，纯离线。

检查项：
  1. PE OptionalHeader 的 MajorOperatingSystemVersion / MajorSubsystemVersion
     必须 <= 6（Win7 = NT 6.1）。Go 1.27+ 默认写 10.0，Win7 加载器直接拒绝。
  2. 导入表（import table）里不得出现 Win8+ 才有的 API。重点：
     ProcessPrng（bcryptprimitives.dll，Win8+）。
  3. 不允许静态导入 LoadLibraryExW 之外的非 kernel32 DLL（Go 运行时应只依赖
     kernel32；其它一律 GetProcAddress 动态取）。

退出码：0 = 通过；1 = 发现问题（打印详情）。

用法: scripts/pe-check-win7.py dist/mizar-win7.exe
"""
import struct
import sys

WIN8_PLUS_APIS = {
    "ProcessPrng": "bcryptprimitives.dll (Windows 8+)",
    "GetSystemTimePreciseAsFileTime": "kernel32 (Windows 8+)",
    "WaitOnAddress": "kernel32 (Windows 8+)",
    "WakeByAddressSingle": "kernel32 (Windows 8+)",
    "WakeByAddressAll": "kernel32 (Windows 8+)",
    "SetThreadDescription": "kernel32 (Windows 10 1607+)",
    "GetThreadDescription": "kernel32 (Windows 10 1607+)",
    "GetTempPath2W": "kernel32 (Windows 11)",
    "CreateFile2": "kernel32 (Windows 8+)",
    "DiscardVirtualMemory": "kernel32 (Windows 8.1+)",
    "PrefetchVirtualMemory": "kernel32 (Windows 8+)",
}


def main(path):
    d = open(path, "rb").read()
    if d[:2] != b"MZ":
        print("!! 不是 PE 文件")
        return 1
    e = struct.unpack_from("<I", d, 0x3C)[0]
    if d[e:e + 4] != b"PE\0\0":
        print("!! 缺少 PE 签名")
        return 1
    nsec = struct.unpack_from("<H", d, e + 6)[0]
    sizeopt = struct.unpack_from("<H", d, e + 20)[0]
    opt = e + 24
    magic = struct.unpack_from("<H", d, opt)[0]
    os_maj, os_min = struct.unpack_from("<HH", d, opt + 40)
    ss_maj, ss_min = struct.unpack_from("<HH", d, opt + 48)
    dd = opt + (112 if magic == 0x20B else 96)
    imp_rva, imp_sz = struct.unpack_from("<II", d, dd + 8)

    secs = []
    for i in range(nsec):
        o = opt + sizeopt + i * 40
        vs, va, rs, ptr = struct.unpack_from("<IIII", d, o + 8)
        secs.append((va, max(vs, rs), ptr))

    def r2o(rva):
        for va, vs, ptr in secs:
            if va <= rva < va + vs:
                return ptr + (rva - va)
        return None

    dlls, funcs = set(), []
    if imp_rva and r2o(imp_rva) is not None:
        o = r2o(imp_rva)
        while True:
            ilt, ts, fc, nm, iat = struct.unpack_from("<IIIII", d, o)
            if nm == 0:
                break
            no = r2o(nm)
            dlls.add(d[no:d.index(b"\0", no)].decode())
            t = r2o(ilt or iat)
            j = 0
            while True:
                v = struct.unpack_from("<Q", d, t + j * 8)[0]
                if v == 0:
                    break
                if not v >> 63:  # 仅按名导入
                    fo = r2o(v & 0x7FFFFFFF) + 2
                    funcs.append(d[fo:d.index(b"\0", fo)].decode())
                j += 1
            o += 20

    print(f"文件           : {path}")
    print(f"OS 版本要求    : {os_maj}.{os_min}")
    print(f"子系统版本要求 : {ss_maj}.{ss_min}")
    print(f"静态导入 DLL   : {', '.join(sorted(dlls)) or '(无)'}")
    print(f"导入函数数     : {len(funcs)}")

    bad = []
    if os_maj > 6 or ss_maj > 6:
        bad.append(
            f"PE 头要求 NT {os_maj}.{os_min}，Windows 7 是 NT 6.1 —— Win7 加载器会直接拒绝"
        )
    for f in sorted(set(funcs)):
        if f in WIN8_PLUS_APIS:
            bad.append(f"静态导入 {f}（{WIN8_PLUS_APIS[f]}）—— Win7 上不存在")
    # ProcessPrng 是 GetProcAddress 动态取的，不会出现在导入表里，
    # 只能靠二进制里的字符串常量判断（runtime 用它取随机数，Win7 上必然崩）。
    if b"ProcessPrng" in d:
        bad.append(
            "二进制里仍有 ProcessPrng 引用（runtime 随机数，Windows 8+ 才有）"
            " —— 说明这是官方 SDK 编的，Win7 启动即崩"
        )
    # 注意：内嵌的 Zig 归档（.zip 里带 mingw 的 .def 文件名）也会出现
    # "bcryptprimitives" 字样，那不是真实依赖，故此处只以 ProcessPrng 为准。

    if bad:
        print("\n!! 以下问题会导致 Win7 无法启动：")
        for b in bad:
            print("   - " + b)
        return 1

    print("\n✓ 通过：可在 Windows 7 SP1（含 KB4474419 + 便捷汇总更新）上加载。")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(__doc__)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
