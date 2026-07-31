package model

import (
	"context"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
)

// 编译期确认本 Adapter 可以被组装根驱动。
var _ registry.Executor = (*Adapter)(nil)

// ExecuteCapability 实现 registry.Executor。
//
// 这是 registry 侧的中性预算类型与本包类型之间唯一的转换处。两组字段必须
// 一一对应：漏掉 MaxRequests 会让次数上限静默失效，而超出预算的调用在
// 账本上和正常调用长得一模一样。
func (a *Adapter) ExecuteCapability(
	ctx context.Context, input registry.ExecuteInput,
) (registry.ExecuteOutput, error) {
	executed, err := a.Execute(ctx, ExecuteRequest{
		Operation: input.Operation,
		Input:     input.Input,
		Budget: Budget{
			MaxCostMicros: input.Budget.MaxCostMicros,
			MaxRequests:   input.Budget.MaxRequests,
		},
		Spent: Usage{
			Requests:     input.Spent.Requests,
			InputTokens:  input.Spent.InputTokens,
			OutputTokens: input.Spent.OutputTokens,
			CostMicros:   input.Spent.CostMicros,
			Estimated:    input.Spent.Estimated,
		},
		Credential:  input.Credential,
		OperationID: input.OperationID,
	})
	if err != nil {
		return registry.ExecuteOutput{}, err
	}
	return registry.ExecuteOutput{
		Result: executed.Result,
		Usage: registry.ModelUsage{
			Requests:     executed.Usage.Requests,
			InputTokens:  executed.Usage.InputTokens,
			OutputTokens: executed.Usage.OutputTokens,
			CostMicros:   executed.Usage.CostMicros,
			Estimated:    executed.Usage.Estimated,
		},
	}, nil
}
