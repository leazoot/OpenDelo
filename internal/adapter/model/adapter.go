package model

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * OpenAI / Anthropic Adapter（REQ-ADAPTER-004）。
 *
 * 与另外两个 Adapter 的差别在于它花的是钱而不是资源状态，因此多两件事：
 *
 *  1. **执行前估算费用**，超出预算就拒绝 —— 不截断提示、不改小 max_tokens。
 *     截断会让 Agent 拿到一个看似正常、其实缺了一半上下文的答复。
 *  2. **把调用次数与费用估算带回来**，由调用方写进 Lease 与审计。
 */

// DefaultBaseURL 是两家厂商的默认地址。
const (
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultAnthropicBaseURL = "https://api.anthropic.com/v1"
)

// charsPerToken 是按字符数估 token 的粗略比例。
//
// 估算只用于**预算是否够**这一个判断，宁可估高：估高了最多让用户多确认一次，
// 估低了会让一次超预算的调用溜过去。
const charsPerToken = 3

// Usage 是一次调用的用量，写入 Lease 与审计（REQ-ADAPTER-004 AC2）。
type Usage struct {
	// Requests 是本次消耗的请求次数。
	Requests int
	// InputTokens 与 OutputTokens 在执行前是估算值，执行后若厂商回报了实际值则替换。
	InputTokens  int
	OutputTokens int
	// CostMicros 是费用，单位微元（百万分之一元）。
	CostMicros int64
	// Estimated 为真表示这组数字是估算的，厂商没有回报实际用量。
	Estimated bool
}

// Budget 是本次授权允许花掉的上限。
//
// 零值表示**没有预算**：那样一次调用都不允许，而不是「不限」——
// 「没说上限」与「上限是无穷」在计费上是两件完全不同的事。
type Budget struct {
	MaxCostMicros int64
	MaxRequests   int
}

// Options 是 Adapter 的配置。
type Options struct {
	// Provider 必填，决定端点与请求形状。
	Provider Provider
	// BaseURL 为空时按 Provider 取默认值。
	BaseURL string
	// Client 为空时按 BaseURL 自建。
	Client *registry.Client
}

// Adapter 是模型服务适配器。一个实例只服务一家厂商。
type Adapter struct {
	provider     Provider
	client       *registry.Client
	byOperation  map[string]registry.Capability
	declarations []registry.Capability
}

// New 构造模型 Adapter。厂商认不出来时返回配置错误。
func New(options Options) (*Adapter, error) {
	if _, known := endpoints[options.Provider]; !known {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("认不出的模型服务厂商：" + string(options.Provider))
	}

	declarations := capabilities(options.Provider)
	byOperation := make(map[string]registry.Capability, len(declarations))
	for _, capability := range declarations {
		byOperation[capability.Operation] = capability
	}

	client := options.Client
	if client == nil {
		baseURL := options.BaseURL
		if baseURL == "" {
			baseURL = DefaultOpenAIBaseURL
			if options.Provider == ProviderAnthropic {
				baseURL = DefaultAnthropicBaseURL
			}
		}
		built, err := registry.NewClient(registry.ClientOptions{BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		client = built
	}

	return &Adapter{
		provider: options.Provider, client: client,
		byOperation: byOperation, declarations: declarations,
	}, nil
}

// Service 是厂商名，与 Scope 的 service 维度对应。
func (a *Adapter) Service() string { return string(a.provider) }

func (a *Adapter) Kind() registry.Kind { return registry.KindModel }

// Capabilities 返回两项声明。
func (a *Adapter) Capabilities() []registry.Capability {
	return append([]registry.Capability(nil), a.declarations...)
}

// ExecuteRequest 是一次执行请求。Credential 是明文，调用方负责 Zero。
type ExecuteRequest struct {
	Operation string
	Input     json.RawMessage
	// Budget 是本次授权的花费上限。
	Budget Budget
	// Spent 是这张 Lease 上已经花掉的部分。
	Spent       Usage
	Credential  secret.Value
	OperationID string
}

// ExecuteResult 是执行结果与用量。
//
// 用量单独返回而不是塞进 Result：它要写进 Lease 与审计，而 Result 是
// 返回给 Agent 的内容 —— 两者的去向不同，混在一起迟早有人把用量也发给 Agent。
type ExecuteResult struct {
	Result registry.Result
	Usage  Usage
}

// Estimate 估算一次调用的用量与费用。
//
// 模型不在价目表里时拒绝：估不出费用就等于预算管不住，
// 而「算不出来就先跑一次」正是 Fail Closed 要挡的写法。
func (a *Adapter) Estimate(request ExecuteRequest) (Usage, error) {
	capability, err := a.capability(request)
	if err != nil {
		return Usage{}, err
	}
	if capability.Operation != OpCreateCompletion {
		return Usage{Requests: 1, Estimated: true}, nil
	}

	var body struct {
		Model     string            `json:"model"`
		MaxTokens int               `json:"max_tokens"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if err = json.Unmarshal(request.Input, &body); err != nil {
		return Usage{}, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("推理请求体解析失败").
			WithOperationID(request.OperationID)
	}

	rate, known := prices[body.Model]
	if !known {
		return Usage{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("模型 " + body.Model + " 不在价目表里，费用无法估算").
			WithOperationID(request.OperationID)
	}
	if body.MaxTokens <= 0 {
		return Usage{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("推理请求没有给出 max_tokens，费用无法估算").
			WithOperationID(request.OperationID)
	}

	inputTokens := 0
	for _, message := range body.Messages {
		inputTokens += len(message)/charsPerToken + 1
	}

	return Usage{
		Requests:     1,
		InputTokens:  inputTokens,
		OutputTokens: body.MaxTokens,
		CostMicros: cost(rate.inputPerKilo, inputTokens) +
			cost(rate.outputPerKilo, body.MaxTokens),
		Estimated: true,
	}, nil
}

// Execute 发起一次调用。
//
// 超预算时**拒绝**，不截断提示、不改小 max_tokens：截断会让 Agent 拿到一个
// 看似正常、其实缺了一半上下文的答复，而它没有办法知道。
func (a *Adapter) Execute(
	ctx context.Context, request ExecuteRequest,
) (ExecuteResult, error) {
	capability, err := a.capability(request)
	if err != nil {
		return ExecuteResult{}, err
	}

	estimate, err := a.Estimate(request)
	if err != nil {
		return ExecuteResult{}, err
	}
	if err = withinBudget(request, estimate); err != nil {
		return ExecuteResult{}, err
	}

	outbound := registry.Request{
		Capability:  capability,
		Path:        capability.Path,
		AuthScheme:  a.authScheme(),
		AuthHeader:  a.authHeader(),
		Credential:  request.Credential,
		OperationID: request.OperationID,
	}
	if capability.Write() {
		outbound.Body = request.Input
	}

	response, err := a.client.Do(ctx, outbound)
	if err != nil {
		return ExecuteResult{
			Result: registry.Failure(request.OperationID, err),
			Usage:  estimate,
		}, nil
	}
	if response.StatusCode >= 400 {
		return ExecuteResult{
			Result: registry.Failure(request.OperationID,
				upstreamError(response.StatusCode, request.OperationID)),
			Usage: estimate,
		}, nil
	}

	data, err := registry.Redact(response.Body, capability)
	if err != nil {
		return ExecuteResult{
			Result: registry.Failure(request.OperationID, err),
			Usage:  estimate,
		}, nil
	}

	return ExecuteResult{
		Result: registry.Success(request.OperationID, data, nil),
		Usage:  actualUsage(response.Body, request.Input, estimate),
	}, nil
}

func (a *Adapter) capability(request ExecuteRequest) (registry.Capability, error) {
	capability, found := a.byOperation[request.Operation]
	if !found {
		// 账单、成员、Key 管理、组织设置都落在这里：它们从来没有被声明过
		// （REQ-ADAPTER-004 AC1）。
		return registry.Capability{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail(string(a.provider) + " 没有声明操作 " + request.Operation).
			WithOperationID(request.OperationID)
	}
	return capability, nil
}

// authScheme 是两家的注入方式：OpenAI 用 Bearer，Anthropic 用自定义头。
func (a *Adapter) authScheme() registry.AuthScheme {
	if a.provider == ProviderAnthropic {
		return registry.AuthHeader
	}
	return registry.AuthBearer
}

func (a *Adapter) authHeader() string {
	if a.provider == ProviderAnthropic {
		return "x-api-key"
	}
	return ""
}

// withinBudget 判断这次调用花不花得起。
//
// 预算为零视为「没有预算」而不是「不限」：一次调用都不允许。
func withinBudget(request ExecuteRequest, estimate Usage) error {
	budget := request.Budget

	if budget.MaxRequests <= 0 || budget.MaxCostMicros <= 0 {
		return overBudget("本次授权没有给出预算上限", request.OperationID)
	}
	if request.Spent.Requests+estimate.Requests > budget.MaxRequests {
		return overBudget("调用次数会超过本次授权的上限", request.OperationID)
	}
	if request.Spent.CostMicros+estimate.CostMicros > budget.MaxCostMicros {
		return overBudget("估算费用 "+micros(estimate.CostMicros)+
			" 加上已花的 "+micros(request.Spent.CostMicros)+
			" 会超过上限 "+micros(budget.MaxCostMicros), request.OperationID)
	}
	return nil
}

func overBudget(detail, operationID string) error {
	return apperr.New(apperr.CodeForbidden).
		WithDetail("超出预算，请求已拒绝：" + detail).
		WithOperationID(operationID)
}

// actualUsage 用厂商回报的实际用量替换估算值。
//
// 回报不全就沿用估算：一个少算了的用量会让预算在下一次调用时管不住。
func actualUsage(body, input json.RawMessage, estimate Usage) Usage {
	var reported struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reported); err != nil {
		return estimate
	}

	inputTokens := reported.Usage.PromptTokens + reported.Usage.InputTokens
	outputTokens := reported.Usage.CompletionTokens + reported.Usage.OutputTokens
	if inputTokens == 0 && outputTokens == 0 {
		return estimate
	}

	name := reported.Model
	if name == "" {
		var requested struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(input, &requested); err == nil {
			name = requested.Model
		}
	}
	rate, known := prices[name]
	if !known {
		return estimate
	}

	return Usage{
		Requests:     estimate.Requests,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostMicros: cost(rate.inputPerKilo, inputTokens) +
			cost(rate.outputPerKilo, outputTokens),
	}
}

// cost 按每千 token 的单价算费用，向上取整。
//
// 向上取整而不是四舍五入：预算是上限，算少了就等于放行了一次本该拒绝的调用。
func cost(perKilo int64, tokens int) int64 {
	if tokens <= 0 {
		return 0
	}
	return (perKilo*int64(tokens) + 999) / 1000
}

func micros(value int64) string {
	return strconv.FormatInt(value, 10) + " 微元"
}

// upstreamError 把厂商的状态码折成对外的错误码，不带原始报文。
func upstreamError(status int, operationID string) error {
	code := apperr.CodeGatewayUnavailable
	switch {
	case status == 401 || status == 403:
		code = apperr.CodeCredentialNotAuthorized
	case status == 404:
		code = apperr.CodeNotFound
	case status == 429:
		code = apperr.CodeConflict
	case status < 500:
		code = apperr.CodeInvalidRequest
	}
	return apperr.New(code).
		WithDetail("模型服务返回了一个失败状态").
		WithOperationID(operationID)
}
