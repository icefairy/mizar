# Mizar 构建脚本
# 用法：
#   make           本机构建（strip，纯静态）并安装到 /usr/local/bin
#   make debug     本机构建（带调试信息，未 strip）
#   make test      全量测试
#   make release   三平台 strip 构建到 dist/（默认发布流程，纯静态）
#   make release-xz 三平台构建 + xz 压缩（发布上传用，体积减少 40%+）
#   make upx       UPX 压缩（仅在明确要求压缩时用，平时不用）
#   make clean     清理 dist/
#
# 发布策略：默认只 strip（-s -w + -trimpath），不压 UPX。
# UPX 压缩会显著拖慢构建且压缩产物不利于符号调试，除非显式要求否则不做。
# 但**上传到 GitHub 的资产一律用 xz 压缩**（release-xz）：内嵌 Zig 工具链后
# 未压缩 126/119/168 MB，上传链路极易超时；xz 后约 74/68/110 MB。
#
# 静态编译策略：项目无真实 import "C"，强制 CGO_ENABLED=0 产出纯静态二进制，
# 不依赖宿主 GLIBC，可跨发行版部署（解决 CentOS 7 / glibc 2.17 报
# "GLIBC_2.32 not found"、Alibaba Cloud Linux 等老 GLIBC 系统的问题）。
# GOAMD64=v1 限定 x86-64 基线指令集，兼容老 CPU（避免 SIGILL）。
# ⚠ 发布必须走 make release，勿手工 go build（手工易漏 CGO_ENABLED=0 导致动态链接）。
# 如需临时回到动态编译用 make dynamic。

BINARY  := mizar
PKG     := ./cmd/mizar
LDFLAGS := -s -w
TRIM    := -trimpath
GOENV   := CGO_ENABLED=0 GOAMD64=v1

.PHONY: all build debug dynamic test release release-amd64 release-arm64 release-windows release-xz release-xz-amd64 release-xz-arm64 release-xz-windows upx clean zig-kernels zig-fetch zig-embed zig-select zig-clean

all: build

# 重新编译 Zig→wasm 计算内核（产物 *.wasm 已内嵌进二进制并纳入版本管理）。
# 只有改动 internal/wasmkern/*.zig 时才需要跑；没有 zig 也能正常构建/发布，
# 因为 .wasm 产物是提交到仓库的。CI 与发布流程不依赖 zig。
# 安装 zig：https://ziglang.org/download/ （单个 55MB 包即可，无需其它工具链）
ZIG ?= zig
zig-kernels:
	@command -v $(ZIG) >/dev/null 2>&1 || { echo "zig 未安装，跳过内核重编（使用已提交的 .wasm）"; exit 0; }
	@for src in internal/wasmkern/*.zig; do \
		name=$$(basename $$src .zig); \
		echo "  zig: $$name"; \
		$(ZIG) build-exe $$src -target wasm32-freestanding -OReleaseFast \
			--name $$name -fno-entry -rdynamic -femit-bin=internal/wasmkern/$$name.wasm; \
	done
	@ls -l internal/wasmkern/*.wasm | awk '{print "  "$$5, $$9}'

build:
	$(GOENV) go build -ldflags="$(LDFLAGS)" $(TRIM) -o $(BINARY) $(PKG)
	@install -m 0755 $(BINARY) /usr/local/bin/$(BINARY) && echo "已安装: /usr/local/bin/$(BINARY)"

# 本机构建（带调试信息，适合 gdb/dlv；静态）
debug:
	CGO_ENABLED=0 go build -o $(BINARY) $(PKG)

# 动态编译（本地调试需要 cgo 时用）
dynamic:
	go build -ldflags="$(LDFLAGS)" $(TRIM) -o $(BINARY) $(PKG)

test:
	go test ./... -count=1

# 三平台 strip 构建（发布用，全部纯静态，可跨发行版部署）
#
# Zig 归档按架构分别内嵌：linux/amd64 只嵌 x86_64-linux，linux/arm64 只嵌
# aarch64-linux，windows/amd64 只嵌 x86_64-windows——每个二进制只带自己那份。
# go:embed all:zigdist 是「目录级」的，无法按 GOARCH 条件排除，
# 所以用 scripts/zigdist-select.sh 在每个二进制构建前重排目录内容。
# 归档不在 ~/.cache/zig 时自动跳过内嵌（二进制照常产出，/zig 会提示未内嵌）。
release:
	@mkdir -p dist
	@echo ">>> linux/amd64（内嵌 x86_64-linux Zig）"
	@scripts/zigdist-select.sh x86_64-linux || true
	GOOS=linux   GOARCH=amd64 $(GOENV) go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-amd64 $(PKG)
	@echo ">>> linux/arm64（内嵌 aarch64-linux Zig）"
	@scripts/zigdist-select.sh aarch64-linux || true
	GOOS=linux   GOARCH=arm64 $(GOENV) go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-arm64 $(PKG)
	@echo ">>> windows/amd64（内嵌 x86_64-windows Zig）"
	@scripts/zigdist-select.sh x86_64-windows || true
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-windows-amd64.exe $(PKG)
	@scripts/zigdist-select.sh all >/dev/null 2>&1 || true
	@echo "--- dist/ ---"
	@ls -lh dist/ | awk '{print $$5, $$9}'

# 单平台发布：只编 linux/amd64 或 linux/arm64，各自内嵌对应 Zig。
# 用法: make release-amd64 / make release-arm64
release-amd64:
	@mkdir -p dist
	@scripts/zigdist-select.sh x86_64-linux || true
	GOOS=linux GOARCH=amd64 $(GOENV) go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-amd64 $(PKG)
	@ls -lh dist/$(BINARY)-linux-amd64 | awk '{print $$5, $$9}'

release-arm64:
	@mkdir -p dist
	@scripts/zigdist-select.sh aarch64-linux || true
	GOOS=linux GOARCH=arm64 $(GOENV) go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-arm64 $(PKG)
	@ls -lh dist/$(BINARY)-linux-arm64 | awk '{print $$5, $$9}'

release-windows:
	@mkdir -p dist
	@scripts/zigdist-select.sh x86_64-windows || true
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-windows-amd64.exe $(PKG)
	@ls -lh dist/$(BINARY)-windows-amd64.exe | awk '{print $$5, $$9}'

# release + UPX 压缩（显式三平台，避免误压 .upx 临时文件）
UPX_TARGETS := dist/$(BINARY)-linux-amd64 dist/$(BINARY)-linux-arm64 dist/$(BINARY)-windows-amd64.exe
upx: release
	@command -v upx >/dev/null 2>&1 || { echo "upx 未安装，先执行: apt install upx-ucl"; exit 1; }
	@for f in $(UPX_TARGETS); do \
		[ -f "$$f" ] || continue; \
		if upx -t "$$f" >/dev/null 2>&1; then echo "  UPX: $$f (已压缩，跳过)"; \
		else upx --best "$$f" | tail -1; fi; \
	done
	@echo "--- 压缩后 ---"
	@ls -lh dist/ | awk '{print $$5, $$9}'

clean:
	rm -rf dist $(BINARY)

# ---- release + xz 压缩（发布上传用）----
# 为什么要压缩：内嵌 Zig 工具链后未压缩产物 126/119/168 MB，本地上传到
# GitHub 的链路很容易被重置或触发「60 秒无数据」超时。xz -6 压到约 74/68/110 MB，
# 传输量减少 40%+，成功率高得多。
# 用法: make release-xz            （三平台）
#       make release-xz-arm64     （只 arm64）
release-xz: release
	@for f in $(UPX_TARGETS); do \
		[ -f "$$f" ] || continue; \
		xz -T0 -6 -k -f "$$f" && \
		printf "  %-28s %5s MB -> %5s MB\n" "$$(basename $$f)" \
			$$(( $$(stat -c %s "$$f") / 1048576 )) \
			$$(( $$(stat -c %s "$$f.xz") / 1048576 )); \
	done
	@echo "--- dist/ ---"
	@ls -lh dist/*.xz | awk '{print $$5, $$9}'

release-xz-arm64: release-arm64
	@xz -T0 -6 -k -f dist/$(BINARY)-linux-arm64 && ls -lh dist/$(BINARY)-linux-arm64.xz | awk '{print $$5, $$9}'

release-xz-amd64: release-amd64
	@xz -T0 -6 -k -f dist/$(BINARY)-linux-amd64 && ls -lh dist/$(BINARY)-linux-amd64.xz | awk '{print $$5, $$9}'

release-xz-windows: release-windows
	@xz -T0 -6 -k -f dist/$(BINARY)-windows-amd64.exe && ls -lh dist/$(BINARY)-windows-amd64.exe.xz | awk '{print $$5, $$9}'

# ---- Zig 工具链（内网现场编译用）----
# 下载官方 Zig 归档到 ~/.cache/zig（优先复用本地缓存，不联网）。
zig-fetch:
	scripts/fetch-zig.sh

# 下载（或复用缓存）并把归档复制到 internal/zigboot/zigdist/，随后 make build
# 会把归档 go:embed 进二进制，目标机执行 /zig init 即可释放工具链。
# 注意：二进制会增大约 55~60 MB；归档目录已被 .gitignore 忽略。
zig-embed:
	scripts/fetch-zig.sh --embed

# 选择哪些平台的归档参与 go:embed（默认把已下载的全放进去）。
# 用法: make zig-select PLAT=x86_64-linux   /  PLAT=aarch64-linux  /  PLAT=none  /  PLAT=all
zig-select:
	scripts/zigdist-select.sh $(or $(PLAT),all)

# 清理本地下载缓存与运行时释放目录。
zig-clean:
	scripts/fetch-zig.sh --clean
