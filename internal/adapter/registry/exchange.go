package registry

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * 一次已授权请求的执行（REQ-PROXY-002）。
 *
 * 这是整个产品里凭据明文唯一经过的一段路：取出 → 注入 → 出站 → 清零。
 * 它落在 adapter 之下不是风格选择 —— `secret.Value` 只允许出现在 credential 与
 * adapter 两个包的签名中（架构测试强制），
 * 因此这段代码放在别处根本编译不过。
 *
 * 对外的 Send **签名里没有 secret.Value**：调用方给的是身份主键，拿回的是脱敏后的
 * 内容。凭据在这个函数里出生、在 defer 里清零，从来不曾属于任何调用方。
 */

// Credentials 按引用取用凭据明文。由 internal/credential/registry 实现。
type Credentials interface {
	Fetch(ctx context.Context, referenceID string) (secret.Value, error)
}

// CredentialReferences 回答「这个身份用的是哪条凭据引用」。
//
// 定义在这里而不是引用 core/matcher 的仓储接口：adapter 不得 import core
// （依赖方向）。实现在组装根。
type CredentialReferences interface {
	ReferenceFor(ctx context.Context, identityID string) (string, error)
}

// ExchangeRequest 是一次已授权请求的执行输入。
//
// 没有凭据字段：凭据由本包按 IdentityID 自己去取。让调用方传进来就等于
// 让明文在 adapter 之外存在过一次。
type ExchangeRequest struct {
	Service     string
	Operation   string
	IdentityID  string
	Resource    map[string]string
	Query       url.Values
	Body        []byte
	OperationID string
}

// ExchangeReply 是脱敏之后、可以交给 Agent 的内容。
type ExchangeReply struct {
	// StatusCode 是给 Agent 的 HTTP 状态码，不是外部服务的原始状态码 ——
	// 后者可能携带外部服务的内部信息。
	StatusCode int
	// UpstreamStatus 是外部服务真正答的状态码，0 表示没有拿到过响应。
	// 只进账本与服务端日志，不进给 Agent 的答复（见 Result.UpstreamStatus）。
	UpstreamStatus int
	Body           []byte
	Usage          ModelUsage
}

// Exchange 执行一次已经拿到 Lease 的请求。
type Exchange struct {
	registry    *Registry
	credentials Credentials
	references  CredentialReferences
}

// NewExchange 校验依赖并构造 Exchange。
//
// 缺任何一项都拒绝构造：一个取不到凭据的 Exchange 会在第一次真实请求时才失败，
// 而那时决策已经做完、Lease 已经签发、账本上已经写下了一次放行。
func NewExchange(registry *Registry, credentials Credentials, references CredentialReferences) (*Exchange, error) {
	switch {
	case registry == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Exchange 缺少 Adapter 注册表")
	case credentials == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Exchange 缺少凭据来源")
	case references == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Exchange 缺少凭据引用解析")
	}
	return &Exchange{registry: registry, credentials: credentials, references: references}, nil
}

// Send 取凭据、执行、返回脱敏结果。
//
// 顺序是有意的：**先确认 Adapter 能执行这个操作，再去取凭据**。反过来的话，
// 一个未声明的操作也会让凭据被取出来一次 —— 取出来就意味着它在内存里存在过，
// 而这一次存在没有任何用处。
func (e *Exchange) Send(ctx context.Context, request ExchangeRequest) (ExchangeReply, error) {
	executor, err := e.executorFor(request.Service)
	if err != nil {
		return ExchangeReply{}, err
	}
	// 只为确认这个操作被声明过，取回的声明本身用不上：请求怎么构造由 Adapter
	// 自己按声明决定。放在取凭据之前，未声明的操作因此不会让凭据被取出来一次。
	if _, declared := e.registry.Capability(request.Service, request.Operation); declared != nil {
		return ExchangeReply{}, declared
	}

	referenceID, err := e.references.ReferenceFor(ctx, request.IdentityID)
	if err != nil {
		return ExchangeReply{}, err
	}
	credential, err := e.credentials.Fetch(ctx, referenceID)
	if err != nil {
		return ExchangeReply{}, err
	}
	// 请求结束即清零，无论成功还是失败。
	defer credential.Zero()

	output, err := executor.ExecuteCapability(ctx, ExecuteInput{
		Operation: request.Operation, Resource: request.Resource, Query: request.Query,
		Input: json.RawMessage(request.Body), Credential: credential,
		OperationID: request.OperationID,
	})
	if err != nil {
		return ExchangeReply{}, err
	}
	return replyOf(output)
}

// executorFor 找出负责该服务、且真的能执行的 Adapter。
//
// 注册得上不等于执行得了：Adapter 接口只要求声明能力。一个只声明不执行的
// Adapter 在这里被明确拒绝，而不是让请求走到一半才发现没人干活。
func (e *Exchange) executorFor(service string) (Executor, error) {
	adapter, err := e.registry.Adapter(service)
	if err != nil {
		return nil, err
	}
	executor, executable := adapter.(Executor)
	if !executable {
		return nil, apperr.New(apperr.CodeNotImplemented).
			WithDetail("服务 " + service + " 的 Adapter 未实现执行")
	}
	return executor, nil
}

// replyOf 把执行结果编码成给 Agent 的答复。
//
// **这里不再脱敏一次。** 脱敏是各 Adapter 在自己的 Execute 里做的（REQ-ADAPTER-007），
// 它们各自有哨兵用例守着。曾经在这里加过一道「保险」，但同一份能力声明跑第二遍
// 与跑一遍结果相同 —— 没有任何用例能区分它在不在，而一道测不出来的保险比没有更糟：
// 它会让人以为第一道漏了还有网接着。
func replyOf(output ExecuteOutput) (ExchangeReply, error) {
	result := output.Result

	encoded, err := json.Marshal(result)
	if err != nil {
		return ExchangeReply{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("执行结果无法序列化").
			WithOperationID(result.OperationID)
	}

	// 状态码只有两种。不透传外部服务的原始状态码 —— 那串数字本身就是外部服务的
	// 内部信息，而 Agent 需要知道的只是「成没成」，原因在已脱敏的 error 里。
	status := 200
	if !result.OK {
		status = 502
	}
	return ExchangeReply{
		StatusCode:     status,
		UpstreamStatus: result.UpstreamStatus,
		Body:           encoded,
		Usage:          output.Usage,
	}, nil
}
