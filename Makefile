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
check: go-check web-check links ## 全部检查：Go（格式/vet/lint/测试）+ 前端（类型/lint/测试/构建）+ 令牌与链接扫描

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

# 量的是产物而不是源码：首屏要下载多少字节只有打包之后才知道。
.PHONY: bundle
bundle: web-build ## 校验首屏包体在 REQ-NFR-001 的预算之内
	node $(CURDIR)/scripts/check-bundle.mjs --verbose

.PHONY: web-check
web-check: web-typecheck web-lint web-test web-build tokens csp bundle ## 仅前端检查

# ---------------------------------------------------------------- 端到端

E2E      := $(CURDIR)/test/e2e
PNPM_E2E := pnpm --dir $(E2E)

# `-tags e2e` 的构建能把出站地址指向本地假服务；分发出去的那份没有这条路
# （internal/cli/outbound_release.go）。名字带 -e2e 是为了在 bin/ 里一眼区分。
E2E_BINARY := $(CURDIR)/bin/opendelo-e2e

$(E2E)/node_modules: $(E2E)/package.json $(E2E)/pnpm-lock.yaml
	$(PNPM_E2E) install --frozen-lockfile
	@touch $@

.PHONY: e2e-install
e2e-install: $(E2E)/node_modules ## 按 lockfile 安装 E2E 依赖与三个自带引擎
	$(PNPM_E2E) exec playwright install --with-deps chromium firefox webkit

.PHONY: e2e-binary
e2e-binary: web-build ## 构建 E2E 用的二进制（含内嵌 Console）
	go build -tags e2e -ldflags "$(LDFLAGS)" -o $(E2E_BINARY) ./cmd/opendelo

.PHONY: e2e-typecheck
e2e-typecheck: $(E2E)/node_modules ## E2E 的类型检查
	$(PNPM_E2E) run typecheck

# 三个引擎都是 Playwright 自带的，装在缓存目录里，不动系统。
# 整套用例只在 chromium 上跑一遍，firefox 与 webkit 只跑兼容性那一份
# （理由见 test/e2e/playwright.config.ts）。
.PHONY: e2e
e2e: e2e-binary e2e-typecheck ## 运行端到端用例（真实二进制 + 本地假外部服务）
	$(PNPM_E2E) exec playwright test --project=chromium --project=firefox --project=webkit

# Edge 单列：`playwright install msedge` 装的是**系统上的那份 Edge**，
# 在开发机上是个侵入性动作，所以不并进 e2e-install。CI 在 Linux 上跑它。
.PHONY: e2e-edge
e2e-edge: e2e-binary e2e-typecheck ## 在 Microsoft Edge 上跑兼容性用例（会安装 Edge）
	$(PNPM_E2E) exec playwright install msedge
	$(PNPM_E2E) exec playwright test --project=msedge

# ---------------------------------------------------------------- 性能基线

# REQ-NFR-001 的三项 Go 侧指标。每条自己带预算，超标即失败 ——
# 「记录一个数字给人看」在没人看的时候等于没有检查。
#
# 不挂在 `make test` 上：三条各要写入十万行前置数据，加起来半分钟以上，
# 而它们要回答的问题每次提交都不会变。
.PHONY: bench
bench: ## 跑 REQ-NFR-001 的基准（决策链路 · Ledger 查询 · 记忆匹配）
	go test ./test/bench/ -bench BenchmarkDecisionChain -run '^$$' -benchtime=500x
	go test ./internal/store/ -bench 'BenchmarkLedgerQuery|BenchmarkTrustMemoryMatch' -run '^$$'

.PHONY: vuln
vuln: $(GOVULNCHECK) ## 扫描依赖漏洞
	$(GOVULNCHECK) ./...

.PHONY: build
build: web-build ## 构建前端产物与二进制
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/opendelo

DIST := $(CURDIR)/dist

# 分发的目标平台。
#
# 全部 CGO_ENABLED=0：SQLite 用的是 modernc 的纯 Go 实现，因此任何一台机器都能
# 编出全部五个，产物也不依赖目标机上的 libc。Windows 只保证编得出来
# （REQ-NFR-003：钥匙串相关功能在那上面不可用）。
DIST_PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: dist
dist: web-build ## 交叉编译可分发二进制到 dist/（含内嵌 Console 与校验和）
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for platform in $(DIST_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		suffix=$$([ "$$os" = windows ] && echo .exe || echo ""); \
		output=$(DIST)/opendelo-$$os-$$arch$$suffix; \
		echo "构建 $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$output ./cmd/opendelo || exit 1; \
	done
	@cd $(DIST) && { command -v sha256sum >/dev/null 2>&1 \
		&& sha256sum opendelo-* > SHA256SUMS \
		|| shasum -a 256 opendelo-* > SHA256SUMS; }
	@echo && ls -lh $(DIST)

.PHONY: links
links: ## 校验文档里的仓库内链接
	node $(CURDIR)/scripts/check-links.mjs --verbose

.PHONY: dev
dev: ## 直接运行 Gateway（开发用）
	go run -ldflags "$(LDFLAGS)" ./cmd/opendelo

.PHONY: clean
clean: ## 删除构建产物（保留 .bin 中的工具与 node_modules）
	rm -rf $(CURDIR)/bin $(DIST) $(WEB)/embedded/dist $(E2E)/test-results $(E2E)/playwright-report
