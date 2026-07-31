package cloudflare

import (
	"context"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
)

// 编译期确认本 Adapter 可以在执行前被查勘。
var _ registry.Previewer = (*Adapter)(nil)

// PreviewSource 实现 registry.Previewer。
//
// 只有会改掉一条已经存在的记录的操作才有旧值可查，而查它们一律走
// 单条 DNS 记录的读取端点 —— 这个操作是 GET，Exchange 会照着能力声明核对。
// 创建与清缓存返回空串：前者改之前那条记录还不存在，后者影响范围无法枚举。
func (a *Adapter) PreviewSource(operation string) string {
	if !changesBefore[operation] {
		return ""
	}
	return OpReadDNSRecord
}

// PreviewCapability 实现 registry.Previewer。
//
// 只做形状转换：走哪条路径、发不发得出去，仍然由 Preview 按能力声明回答。
// AffectedRecords 不带出去 —— 风险引擎从 core/risk 那一侧拿它，
// 一个数字有两个来源迟早会对不上。
func (a *Adapter) PreviewCapability(
	ctx context.Context, input registry.PreviewInput,
) (registry.PreviewOutput, error) {
	preview, err := a.Preview(ctx, ExecuteRequest{
		Operation:   input.Operation,
		Resource:    input.Resource,
		Input:       input.Input,
		Credential:  input.Credential,
		OperationID: input.OperationID,
	})
	if err != nil {
		return registry.PreviewOutput{}, err
	}
	return registry.PreviewOutput{Changes: preview.Changes}, nil
}
