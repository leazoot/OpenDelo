package cloudflare

import (
	"context"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
)

// 编译期确认本 Adapter 可以被组装根驱动。
var _ registry.Executor = (*Adapter)(nil)

// ExecuteCapability 实现 registry.Executor。
//
// 只做形状转换，不新增任何判断：能力是否声明、是否本期实现、路径怎么拼，
// 全部仍由 Execute 回答。Query 与预算在 Cloudflare 上没有对应物，直接丢弃。
func (a *Adapter) ExecuteCapability(
	ctx context.Context, input registry.ExecuteInput,
) (registry.ExecuteOutput, error) {
	result, err := a.Execute(ctx, ExecuteRequest{
		Operation:   input.Operation,
		Resource:    input.Resource,
		Input:       input.Input,
		Credential:  input.Credential,
		OperationID: input.OperationID,
	})
	if err != nil {
		return registry.ExecuteOutput{}, err
	}
	return registry.ExecuteOutput{Result: result}, nil
}
