# OpenDelo 的统一检查入口。本地与 CI 跑同一组 target，避免「本地过了 CI 挂」。
# 工具版本固定在下面的变量里，本地与 CI 因此结果一致。

SHELL := /bin/bash

# 工具装在仓库内，使检查结果不受开发机全局 PATH 影响。
LOCAL_BIN := $(CURDIR)/.bin

GOLANGCI_LINT_VERSION := v2.12.2
GOFUMPT_VERSION       := v0.11.0
GOVULNCHECK_VERSION   := v1.6.0
SQLC_VERSION          := v1.27.0

GOLANGCI_LINT := $(LOCAL_BIN)/golangci-lint
GOFUMPT       := $(LOCAL_BIN)/gofumpt
GOVULNCHECK   := $(LOCAL_BIN)/govulncheck
SQLC          := $(LOCAL_BIN)/sqlc

# sqlc 的产物目录。generate-check 只比对这里，无关改动不会被当成产物过期。
GENERATED_DIR := $(CURDIR)/internal/store/queries

WEB     := $(CURDIR)/web
PNPM    := pnpm --dir $(WEB)

BINARY  := $(CURDIR)/bin/opendelo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X main.version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## 列出可用 target
	@echo "OpenDelo — 可用 target："
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'
	@echo
	@echo "工具版本：golangci-lint $(GOLANGCI_LINT_VERSION) · gofumpt $(GOFUMPT_VERSION) · govulncheck $(GOVULNCHECK_VERSION) · sqlc $(SQLC_VERSION)"

.PHONY: tools
tools: $(GOLANGCI_LINT) $(GOFUMPT) $(GOVULNCHECK) $(SQLC) ## 安装固定版本的检查工具到 .bin/

$(GOLANGCI_LINT):
	GOBIN=$(LOCAL_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOFUMPT):
	GOBIN=$(LOCAL_BIN) go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

$(GOVULNCHECK):
	GOBIN=$(LOCAL_BIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(SQLC):
	GOBIN=$(LOCAL_BIN) go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

.PHONY: generate
generate: $(SQLC) ## 从 SQL 生成类型安全查询代码
	$(SQLC) generate

# 与运行 sqlc 前的快照比对，而不是看 git diff：后者会被暂存区掩盖，也会把
# 「SQL 与产物一起改了」这种正常情况误报成产物过期。
.PHONY: generate-check
generate-check: $(SQLC) ## 校验生成产物与 SQL 一致，不一致即失败
	@snapshot=$$(mktemp -d); \
	trap 'rm -rf "$$snapshot"' EXIT; \
	cp -R $(GENERATED_DIR) "$$snapshot/before"; \
	$(SQLC) generate; \
	if ! difference=$$(diff -r "$$snapshot/before" $(GENERATED_DIR)); then \
		echo "sqlc 生成产物与 SQL 不一致，请运行 make generate 并提交结果："; \
		echo "$$difference"; \
		exit 1; \
	fi

.PHONY: fmt
fmt: $(GOFUMPT) ## 就地格式化 Go 代码
	$(GOFUMPT) -l -w .

.PHONY: fmt-check
fmt-check: $(GOFUMPT) ## 校验格式，有未格式化文件即失败
	@unformatted=$$($(GOFUMPT) -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件未通过 gofumpt 格式化，请运行 make fmt："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## 运行 go vet
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## 运行 golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: test
test: ## 运行全部 Go 测试（含竞态检测）
	go test ./... -race

.PHONY: check
check: go-check web-check ## 全部检查：Go（格式/vet/lint/测试）+ 前端（类型/lint/测试/构建）+ 令牌扫描

.PHONY: go-check
go-check: fmt-check vet lint test ## 仅 Go 侧检查

# ---------------------------------------------------------------- 前端

$(WEB)/node_modules: $(WEB)/package.json $(WEB)/pnpm-lock.yaml
	$(PNPM) install --frozen-lockfile
	@touch $@

.PHONY: web-install
web-install: $(WEB)/node_modules ## 按 lockfile 安装前端依赖

.PHONY: web-typecheck
web-typecheck: web-install ## 前端类型检查
	$(PNPM) run typecheck

.PHONY: web-lint
web-lint: web-install ## 前端 lint
	$(PNPM) run lint

.PHONY: web-test
web-test: web-install ## 前端测试
	$(PNPM) run test

.PHONY: web-build
web-build: web-install ## 构建 Console 产物到 web/embedded/dist
	$(PNPM) run build

.PHONY: tokens
tokens: ## 扫描 web/src 下的字面色值
	node $(CURDIR)/scripts/check-tokens.mjs --verbose

# 扫的是产物而不是源码：内联与否是打包器的决定，源码里看不出来。
.PHONY: csp
csp: web-build ## 校验 Console 产物能在 Gateway 下发的 CSP 之下工作
	node $(CURDIR)/scripts/check-csp.mjs --verbose

.PHONY: web-check
web-check: web-typecheck web-lint web-test web-build tokens csp ## 仅前端检查

.PHONY: vuln
vuln: $(GOVULNCHECK) ## 扫描依赖漏洞
	$(GOVULNCHECK) ./...

.PHONY: build
build: web-build ## 构建前端产物与二进制
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/opendelo

.PHONY: dev
dev: ## 直接运行 Gateway（开发用）
	go run -ldflags "$(LDFLAGS)" ./cmd/opendelo

.PHONY: clean
clean: ## 删除构建产物（保留 .bin 中的工具与 node_modules）
	rm -rf $(CURDIR)/bin $(WEB)/embedded/dist
