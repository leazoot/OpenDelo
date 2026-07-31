package generichttp

import (
	"context"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
)

// 编译期确认本 Adapter 可以被组装根驱动。
var _ registry.Executor = (*Adapter)(nil)

// ExecuteCapability 实现 registry.Executor。
//
// 只做形状转换。Query 在这里必须传下去：用户定义的操作可以把查询参数
// 写进声明，丢掉它会让请求打到一个没有过滤条件的端点上。
func (a *Adapter) ExecuteCapability(
	ctx context.Context, input registry.ExecuteInput,
) (registry.ExecuteOutput, error) {
	result, err := a.Execute(ctx, ExecuteRequest{
		Operation:   input.Operation,
		Resource:    input.Resource,
		Query:       input.Query,
		Input:       input.Input,
		Credential:  input.Credential,
		OperationID: input.OperationID,
	})
	if err != nil {
		return registry.ExecuteOutput{}, err
	}
	return registry.ExecuteOutput{Result: result}, nil
}
