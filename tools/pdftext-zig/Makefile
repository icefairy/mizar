# tools/pdftext-zig — 纯 Zig 单文件 PDF 文本提取器
#
# 用法：
#   make            编译出 ./pdftext（ReleaseSmall，约 260KB）
#   make test       运行单元测试
#   make clean      清理产物
#
# 依赖：Zig 0.14+（本项目内置的 0.17.1 可用；也可用 mizar 自带 /zig 释放的工具链）
# 说明：不使用 build.zig（部分环境下 zig build 会卡在 cache 初始化），
#      直接 zig build-exe 更稳、更快。

ZIG  ?= zig
OUT  ?= pdftext
SRC  ?= pdftext.zig
FLAGS := -OReleaseSmall

.PHONY: all build test clean run

all: build

build:
	$(ZIG) build-exe $(SRC) $(FLAGS) --name $(OUT)
	@echo "已生成: ./$(OUT)  $$(du -h $(OUT) | cut -f1)"

test:
	$(ZIG) test $(SRC) $(FLAGS)

# 用法: make run FILE=xxx.pdf
run: build
	./$(OUT) $(FILE)

clean:
	rm -f $(OUT) $(OUT).o $(OUT).o.d
