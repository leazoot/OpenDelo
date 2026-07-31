package registry

import (
	"context"
	"encoding/json"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * 执行前的查勘（REQ-APPROVAL-001 AC4）。
 *
 * 审批页面要回答「这个值现在是什么」。答案只能来自一次真实查询 —— 编不出来，
 * 也不能从请求里推：请求里写的是**将要**变成什么。
 *
 * 这条路径与 Send 共用同一段凭据处理：取出 → 注入 → 出站 → 清零，明文同样
 * 不曾属于任何调用方。但它比 Send 多两道闸，因为它发生在**人还没同意之前**：
 *
 *  1. 查旧值只能走一个**已声明的只读操作**。哪个操作由 Adapter 报出来，
 *     由本包按能力声明核对；核对不过就一个字节都不发出去。
 *  2. 拿不到旧值不是失败，是「没有旧值可展示」。决策链路照常走完 ——
 *     一次查询失败让整条请求失败，等于把外部服务的可用性接进了决策链路。
 *     这一条由调用方保证，本包如实返回错误。
 */

// PreviewInput 是一次查勘的输入。
//
// 与 ExecuteInput 分开：查勘不需要 Query，也不需要模型的预算与用量 ——
// 那些字段在这里没有含义，留着只会让人以为查勘也能花钱。
type PreviewInput struct {
	// Operation 是**要被审批的那个写操作**，不是查询用的那个。
	// 走哪个只读操作去查由 Adapter 按 PreviewSource 决定。
	Operation string
	// Resource 是资源维度的取值，与将要执行的那次请求一致。
	Resource map[string]string
	// Input 是请求体，用来算出「将要变成什么」。
	Input json.RawMessage
	// Credential 是凭据明文。调用方负责 Zero。
	Credential secret.Value
	// OperationID 随结果与错误一起回到账本。
	OperationID string
}

// PreviewOutput 是一次查勘的结果。
//
// 只有 Changes：影响记录数由风险引擎在别处算，放进来会出现第二个来源。
type PreviewOutput struct {
	Changes []ResourceChange
}

// Previewer 是「能在执行前查出旧值」的 Adapter。
//
// 嵌入 Adapter 的理由与 Executor 相同：一个能查勘却没声明能力的类型不该存在。
// 不实现本接口的 Adapter 只是没有旧值可展示，不影响它执行 —— 审批页面照实说
// 「当前值尚未查询」，而不是编一个出来。
type Previewer interface {
	Adapter
	// PreviewSource 报告查这个写操作的旧值要走哪一个**已声明的只读操作**。
	//
	// 返回空串表示这个操作没有旧值可查（新建、清缓存这类）。返回的操作名由
	// Exchange 按能力声明核对：认不出、或者它其实是个写操作，查勘一律不发出。
	//
	// 做成「报出来再核对」而不是「Adapter 自己保证」：后者的正确性要靠每个
	// Adapter 各自记得，而这一条一旦失守，查勘就成了绕过端点白名单的入口。
	PreviewSource(operation string) string
	// PreviewCapability 走 PreviewSource 指出的那个只读操作查出当前值。
	//
	// 实现**不得**发出任何写请求：调用方已经核对过声明，但那道核对挡住的是
	// 声明层面的错误，实现层面的越界只能由实现自己守住。
	PreviewCapability(ctx context.Context, input PreviewInput) (PreviewOutput, error)
}

// Preview 查出这次请求会改掉的那些字段的当前值。
//
// 顺序与 Send 一致，理由也一致：**先确认这次查勘合法，再去取凭据**。
// 没有旧值可查的操作因此连凭据都不会被取出来一次。
func (e *Exchange) Preview(ctx context.Context, request ExchangeRequest) (PreviewOutput, error) {
	previewer, err := e.previewerFor(request.Service)
	if err != nil {
		return PreviewOutput{}, err
	}
	if _, declared := e.registry.Capability(request.Service, request.Operation); declared != nil {
		return PreviewOutput{}, declared
	}

	source := previewer.PreviewSource(request.Operation)
	if source == "" {
		return PreviewOutput{}, nil
	}
	if err = e.readOnlySource(request.Service, source); err != nil {
		return PreviewOutput{}, err
	}

	referenceID, err := e.references.ReferenceFor(ctx, request.IdentityID)
	if err != nil {
		return PreviewOutput{}, err
	}
	credential, err := e.credentials.Fetch(ctx, referenceID)
	if err != nil {
		return PreviewOutput{}, err
	}
	defer credential.Zero()

	return previewer.PreviewCapability(ctx, PreviewInput{
		Operation: request.Operation, Resource: request.Resource,
		Input: json.RawMessage(request.Body), Credential: credential,
		OperationID: request.OperationID,
	})
}

// readOnlySource 核对查勘要走的那个操作确实被声明过、且确实是只读。
//
// 四道闸的第一道。写操作即使被声明过也不行：
// 「查一下当前值」在人还没同意之前发生，它能改变外部状态就等于绕过了审批。
func (e *Exchange) readOnlySource(service, operation string) error {
	capability, err := e.registry.Capability(service, operation)
	if err != nil {
		return err
	}
	if capability.Write() {
		return apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("查勘只能走只读操作，" + service + " 的 " + operation +
				" 是 " + capability.Method + " 请求")
	}
	return nil
}

// previewerFor 找出负责该服务、且真的能查勘的 Adapter。
func (e *Exchange) previewerFor(service string) (Previewer, error) {
	adapter, err := e.registry.Adapter(service)
	if err != nil {
		return nil, err
	}
	previewer, previewable := adapter.(Previewer)
	if !previewable {
		return nil, apperr.New(apperr.CodeNotImplemented).
			WithDetail("服务 " + service + " 的 Adapter 不支持执行前查勘")
	}
	return previewer, nil
}
