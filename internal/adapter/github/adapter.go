package github

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * GitHub Adapter（REQ-ADAPTER-002）。
 *
 * 它做三件事：把「操作 + 资源」翻译成一条被声明过的请求、把凭据交给出站通道注入、
 * 把响应脱敏成允许返回给 Agent 的样子。
 *
 * **它不决定是否允许。** 走到这里时决策已经做完、Lease 已经签发
 */

// DefaultBaseURL 是 GitHub REST API 的地址。
const DefaultBaseURL = "https://api.github.com"

// fieldLogs 是 Actions 日志在返回体里的字段名。日志是纯文本，
// 包装成一个字段才能和其余能力共用同一个 Result 形状。
const fieldLogs = "logs"

// Options 是 Adapter 的配置。
type Options struct {
	// BaseURL 为空时用 DefaultBaseURL。用例用它指向本地假服务。
	BaseURL string
	// Client 为空时按 BaseURL 自建。
	Client *registry.Client
}

// Adapter 是 GitHub 服务适配器。
type Adapter struct {
	client       *registry.Client
	byOperation  map[string]registry.Capability
	declarations []registry.Capability
}

// New 构造 GitHub Adapter。
func New(options Options) (*Adapter, error) {
	declarations := capabilities()
	byOperation := make(map[string]registry.Capability, len(declarations))
	for _, capability := range declarations {
		byOperation[capability.Operation] = capability
	}

	client := options.Client
	if client == nil {
		baseURL := options.BaseURL
		if baseURL == "" {
			baseURL = DefaultBaseURL
		}
		built, err := registry.NewClient(registry.ClientOptions{BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		client = built
	}

	return &Adapter{client: client, byOperation: byOperation, declarations: declarations}, nil
}

func (a *Adapter) Service() string { return Service }

func (a *Adapter) Kind() registry.Kind { return registry.KindGitHub }

func (a *Adapter) BaseURL() string { return a.client.BaseURL() }

// AuthScheme 是 Bearer：凭据以 Authorization 头注入，形式记在声明里，
// 值只在执行的那一刻从 Provider 取。
func (a *Adapter) AuthScheme() registry.AuthScheme { return registry.AuthBearer }

// Capabilities 返回全部十三项声明。
func (a *Adapter) Capabilities() []registry.Capability {
	return append([]registry.Capability(nil), a.declarations...)
}

// ExecuteRequest 是一次执行请求。
//
// Credential 是明文，调用方负责 Zero。
type ExecuteRequest struct {
	Operation   string
	Resource    map[string]string
	Input       json.RawMessage
	Credential  secret.Value
	OperationID string
}

// Execute 执行一项 MVP 能力。
//
// 五项高风险操作返回 not_implemented：本期只声明风险，不实现执行
// 返回未实现错误而不是悄悄成功 ——
// 一个「看起来做了」的合并主分支比明确的失败危险得多。
func (a *Adapter) Execute(
	ctx context.Context, request ExecuteRequest,
) (registry.Result, error) {
	capability, found := a.byOperation[request.Operation]
	if !found {
		return registry.Result{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("github 没有声明操作 " + request.Operation).
			WithOperationID(request.OperationID)
	}
	if !executable[request.Operation] {
		return registry.Result{}, apperr.New(apperr.CodeNotImplemented).
			WithDetail(request.Operation + " 本期只声明风险等级，未实现执行").
			WithOperationID(request.OperationID)
	}

	path, err := buildPath(capability, request.Resource, request.OperationID)
	if err != nil {
		return registry.Result{}, err
	}

	outbound := registry.Request{
		Capability:  capability,
		Path:        path,
		AuthScheme:  registry.AuthBearer,
		Credential:  request.Credential,
		OperationID: request.OperationID,
	}
	if capability.Write() {
		outbound.Body = request.Input
	}

	response, err := a.client.Do(ctx, outbound)
	if err != nil {
		return registry.Failure(request.OperationID, err), nil
	}
	if response.StatusCode >= 400 {
		return registry.Failure(request.OperationID,
			upstreamError(response.StatusCode, request.OperationID)).
			FromUpstream(response.StatusCode), nil
	}

	data, err := a.render(capability, response.Body)
	if err != nil {
		return registry.Failure(request.OperationID, err).
			FromUpstream(response.StatusCode), nil
	}
	return registry.Success(request.OperationID, data, nil).
		FromUpstream(response.StatusCode), nil
}

// render 把响应体裁成允许返回的内容。
//
// Actions 日志是纯文本，走文本脱敏；其余是 JSON，走白名单加词表。
func (a *Adapter) render(
	capability registry.Capability, body []byte,
) (json.RawMessage, error) {
	if capability.Operation != OpReadActionsLogs {
		return registry.Redact(body, capability)
	}

	cleaned := RedactActionsLog(string(body), capability.RedactionRules)
	encoded, err := json.Marshal(map[string]string{fieldLogs: cleaned})
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("脱敏后的 Actions 日志无法序列化")
	}
	return encoded, nil
}

// githubTokenPattern 匹配 GitHub 自己的令牌前缀。
//
// GitHub 会把它知道的 Secret 打上掩码，但它只认得自己知道的那些：一个从别处
// 粘进构建脚本、被 echo 出来的令牌，掩码不会命中（REQ-ADAPTER-002 AC3）。
var githubTokenPattern = regexp.MustCompile(
	`gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}`)

// RedactActionsLog 对 Actions 日志执行本地脱敏规则。
//
// 两层：先按 GitHub 的令牌形状抹掉裸令牌，再按字段名抹掉赋值。
// 顺序不能反 —— 赋值规则会把 "token=ghp_xxx" 整段换成占位，
// 那样第一层就没有机会证明自己认得裸令牌了。
func RedactActionsLog(text string, extraRules []string) string {
	masked := githubTokenPattern.ReplaceAllString(text, registry.RedactedMarker)
	return registry.RedactText(masked, extraRules)
}

// buildPath 用资源维度填出实际路径。
//
// 逐段校验：一个带斜杠的 owner 能把 /repos/{owner}/{repo} 变成另一个端点，
// 而那个端点没有被声明过。
func buildPath(
	capability registry.Capability, resource map[string]string, operationID string,
) (string, error) {
	segments := strings.Split(strings.TrimPrefix(capability.Path, "/"), "/")
	filled := make([]string, 0, len(segments))

	for _, segment := range segments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			filled = append(filled, segment)
			continue
		}

		key := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		value, present := resource[key]
		switch {
		case !present || strings.TrimSpace(value) == "":
			return "", invalidResource("资源维度 "+key+" 没有取值", operationID)
		case strings.ContainsAny(value, "/?#"), strings.Contains(value, ".."):
			return "", invalidResource("资源维度 "+key+" 的取值会改变请求的端点", operationID)
		}
		filled = append(filled, value)
	}
	return "/" + strings.Join(filled, "/"), nil
}

func invalidResource(detail, operationID string) error {
	return apperr.New(apperr.CodeInvalidRequest).
		WithDetail(detail).
		WithOperationID(operationID)
}

// upstreamError 把 GitHub 的状态码折成对外的错误码。
//
// 不带外部服务的原始报文：那里面可能有请求回显（REQ-ADAPTER-007 AC3）。
func upstreamError(status int, operationID string) error {
	code := apperr.CodeGatewayUnavailable
	switch {
	case status == 401 || status == 403:
		code = apperr.CodeCredentialNotAuthorized
	case status == 404:
		code = apperr.CodeNotFound
	case status == 409:
		code = apperr.CodeConflict
	case status < 500:
		code = apperr.CodeInvalidRequest
	}
	return apperr.New(code).
		WithDetail("GitHub 返回了一个失败状态").
		WithOperationID(operationID)
}
