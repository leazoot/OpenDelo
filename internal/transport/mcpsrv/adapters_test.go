package mcpsrv_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
)

// allAdapters 注册本期实现的三个内建 Adapter。
//
// 用真实声明而不是简化夹具：REQ-MCP-001 AC1 说的「由声明生成」，
// 只有跑在实际会上线的那批声明上才算被验证过。Generic HTTP 不在此列 ——
// 它的声明来自用户配置，没有编译期的一份可测（REQ-ADAPTER-006）。
func allAdapters(t *testing.T) (*registry.Registry, error) {
	t.Helper()

	githubAdapter, err := github.New(github.Options{})
	if err != nil {
		return nil, err
	}
	cloudflareAdapter, err := cloudflare.New(cloudflare.Options{})
	if err != nil {
		return nil, err
	}
	openaiAdapter, err := model.New(model.Options{Provider: model.ProviderOpenAI})
	if err != nil {
		return nil, err
	}
	anthropicAdapter, err := model.New(model.Options{Provider: model.ProviderAnthropic})
	if err != nil {
		return nil, err
	}
	return registry.New(githubAdapter, cloudflareAdapter, openaiAdapter, anthropicAdapter)
}
