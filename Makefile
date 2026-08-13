# Mizar 构建脚本
# 用法：
#   make           本机构建（strip）
#   make debug     本机构建（带调试信息，未 strip）
#   make test      全量测试
#   make release   三平台 strip 构建到 dist/（默认发布流程）
#   make upx       UPX 压缩（仅在明确要求压缩时用，平时不用）
#   make clean     清理 dist/
#
# 发布策略：默认只 strip（-s -w + -trimpath），不压 UPX。
# UPX 压缩会显著拖慢构建且压缩产物不利于符号调试，除非显式要求否则不做。

BINARY  := mizar
PKG     := ./cmd/mizar
LDFLAGS := -s -w
TRIM    := -trimpath

.PHONY: all debug test release upx clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" $(TRIM) -o $(BINARY) $(PKG)

# 本机构建（带调试信息，适合 gdb/dlv）
debug:
	go build -o $(BINARY) $(PKG)

test:
	go test ./... -count=1

# 三平台 strip 构建（发布用）
release:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-amd64 $(PKG)
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-linux-arm64 $(PKG)
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" $(TRIM) -o dist/$(BINARY)-windows-amd64.exe $(PKG)
	@echo "--- dist/ ---"
	@ls -lh dist/ | awk '{print $$5, $$9}'

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
