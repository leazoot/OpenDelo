package generichttp

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * Generic HTTP Adapter（REQ-ADAPTER-005）。
 *
 * 内容由用户填，因此它是四个 Adapter 里唯一需要在**每次请求前**重新校验目标地址的：
 * 定义存下来时解析到公网的域名，下一分钟可以解析到 127.0.0.1（DNS 重绑定）。
 */

// Options 是 Adapter 的配置。
type Options struct {
	// Resolver 为空时走系统解析器。
	Resolver Resolver
	// Client 为空时按定义里的 Base URL 自建。用例用它把出站接到本地假服务上，
	// 而 Base URL 与目标校验照常生效。
	Client *registry.Client
}

// Adapter 是用户自定义的 HTTP 服务适配器。
type Adapter struct {
	definition   Definition
	client       *registry.Client
	resolve      Resolver
	host         string
	byOperation  map[string]registry.Capability
	declarations []registry.Capability
}

// New 校验定义并构造 Adapter。
//
// 三条都在这里成立：操作声明齐全（含风险等级）、Base URL 是 https、
// 目标不指向本机或内网。任何一条不过就存不下来（REQ-ADAPTER-005 AC2）。
func New(ctx context.Context, definition Definition, options Options) (*Adapter, error) {
	if err := definition.validateShape(); err != nil {
		return nil, err
	}

	declared, err := definition.capabilities()
	if err != nil {
		return nil, err
	}

	resolve := options.Resolver
	if resolve == nil {
		resolve = defaultResolver
	}
	if err = validateBaseURL(ctx, definition.BaseURL, resolve); err != nil {
		return nil, err
	}

	client := options.Client
	if client == nil {
		built, buildErr := registry.NewClient(registry.ClientOptions{BaseURL: definition.BaseURL})
		if buildErr != nil {
			return nil, buildErr
		}
		client = built
	}

	parsed, err := url.Parse(definition.BaseURL)
	if err != nil {
		return nil, invalidDefinitionWrap(err, "Base URL 解析失败")
	}

	byOperation := make(map[string]registry.Capability, len(declared))
	for _, capability := range declared {
		byOperation[capability.Operation] = capability
	}

	return &Adapter{
		definition: definition, client: client, resolve: resolve,
		host:        parsed.Hostname(),
		byOperation: byOperation, declarations: declared,
	}, nil
}

// Service 是用户给出的服务名。
func (a *Adapter) Service() string { return a.definition.Service }

func (a *Adapter) Kind() registry.Kind { return registry.KindGenericHTTP }

// Capabilities 返回用户定义的全部操作。
func (a *Adapter) Capabilities() []registry.Capability {
	return append([]registry.Capability(nil), a.declarations...)
}

// ExecuteRequest 是一次执行请求。Credential 是明文，调用方负责 Zero。
type ExecuteRequest struct {
	Operation   string
	Resource    map[string]string
	Query       url.Values
	Input       json.RawMessage
	Credential  secret.Value
	OperationID string
}

// Execute 执行一项用户定义的操作。
//
// 每次都重新校验目标地址：定义存下来时解析到公网的域名，下一分钟可以
// 解析到 127.0.0.1（DNS 重绑定）。
func (a *Adapter) Execute(
	ctx context.Context, request ExecuteRequest,
) (registry.Result, error) {
	capability, found := a.byOperation[request.Operation]
	if !found {
		return registry.Result{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail(a.definition.Service + " 没有声明操作 " + request.Operation).
			WithOperationID(request.OperationID)
	}

	path, err := buildPath(capability, request.Resource, request.OperationID)
	if err != nil {
		return registry.Result{}, err
	}

	if err = checkHost(ctx, a.host, a.resolve); err != nil {
		return registry.Result{}, apperr.New(apperr.CodePathNotAllowed).
			WithDetail("目标地址此刻指向本机或内网，请求已拒绝").
			WithOperationID(request.OperationID)
	}

	outbound := registry.Request{
		Capability:  capability,
		Path:        path,
		Query:       request.Query,
		AuthScheme:  a.definition.AuthScheme,
		AuthHeader:  a.definition.AuthHeader,
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
			upstreamError(response.StatusCode, request.OperationID)), nil
	}

	data, err := registry.Redact(response.Body, capability)
	if err != nil {
		return registry.Failure(request.OperationID, err), nil
	}
	return registry.Success(request.OperationID, data, nil), nil
}

// buildPath 用资源维度填出实际路径，逐段校验。
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

// upstreamError 把外部服务的状态码折成对外的错误码，不带原始报文。
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
		WithDetail("外部服务返回了一个失败状态").
		WithOperationID(operationID)
}
