package cloudflare

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * Cloudflare Adapter（REQ-ADAPTER-003）。
 *
 * 比 GitHub 多做一件事：**改动之前先把当前值查出来**（AC1）。
 * 审批页面要展示旧值 —— 没有旧值，用户是在对一个自己看不见的东西点同意，
 * 而 DNS 改错一条记录就是整个站点解析到别处。
 */

// DefaultBaseURL 是 Cloudflare API v4 的地址。
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// UnknownRecordCount 表示本次影响的记录数无法确定。
//
// 交给 core/risk 的 ResourceCount 用 0 表达「不知道」：写操作遇到它按批量处理
// （Fail Closed），不按「大概是一条」处理。
const UnknownRecordCount = 0

// Options 是 Adapter 的配置。
type Options struct {
	// BaseURL 为空时用 DefaultBaseURL。用例用它指向本地假服务。
	BaseURL string
	// Client 为空时按 BaseURL 自建。
	Client *registry.Client
}

// Adapter 是 Cloudflare 服务适配器。
type Adapter struct {
	client       *registry.Client
	byOperation  map[string]registry.Capability
	declarations []registry.Capability
}

// New 构造 Cloudflare Adapter。
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

func (a *Adapter) Kind() registry.Kind { return registry.KindCloudflare }

func (a *Adapter) BaseURL() string { return a.client.BaseURL() }

// AuthScheme 是 Bearer：凭据以 Authorization 头注入，形式记在声明里，
// 值只在执行的那一刻从 Provider 取。
func (a *Adapter) AuthScheme() registry.AuthScheme { return registry.AuthBearer }

// Capabilities 返回全部声明。
func (a *Adapter) Capabilities() []registry.Capability {
	return append([]registry.Capability(nil), a.declarations...)
}

// ExecuteRequest 是一次执行请求。Credential 是明文，调用方负责 Zero。
type ExecuteRequest struct {
	Operation   string
	Resource    map[string]string
	Input       json.RawMessage
	Credential  secret.Value
	OperationID string
}

// Preview 是执行前的查勘结果，供风险计算与审批展示使用。
type Preview struct {
	// AffectedRecords 是本次会影响的记录数。0 表示无法确定 ——
	// core/risk 对写操作的 ResourceCount != 1 一律判为大范围修改（AC3）。
	AffectedRecords int
	// Changes 是旧值到新值的对照，来自执行前的一次查询（AC1）。
	Changes []registry.ResourceChange
}

// Preview 在执行前查出当前值并报告本次影响多少条记录。
//
// 决策链路在算风险与做审批之前调用它：AffectedRecords 进 risk.Factors，
// Changes 进审批页面。它自己只发**读取**请求，不改变任何东西。
func (a *Adapter) Preview(
	ctx context.Context, request ExecuteRequest,
) (Preview, error) {
	capability, err := a.executableCapability(request)
	if err != nil {
		return Preview{}, err
	}

	if _, err = buildPath(capability, request.Resource, request.OperationID); err != nil {
		return Preview{}, err
	}

	preview := Preview{AffectedRecords: affectedRecords(capability, request.Input)}
	if !changesBefore[request.Operation] {
		return preview, nil
	}

	current, err := a.readCurrentRecord(ctx, request)
	if err != nil {
		return Preview{}, err
	}
	preview.Changes = compare(request.Operation, current, request.Input)
	return preview, nil
}

// Execute 执行一项 MVP 能力。
//
// 对会改掉已有记录的操作，执行路径**自己**先查一次当前值：AC1 要求的是
// 「修改前必须先查询」，把它做成结构上的前置步骤，比指望调用方记得调 Preview 可靠。
func (a *Adapter) Execute(
	ctx context.Context, request ExecuteRequest,
) (registry.Result, error) {
	capability, err := a.executableCapability(request)
	if err != nil {
		return registry.Result{}, err
	}

	// 路径先算出来：资源取值不合法是**请求本身**的问题，不是外部服务的失败，
	// 因此按错误返回而不是折成一个 Failure 结果。
	path, err := buildPath(capability, request.Resource, request.OperationID)
	if err != nil {
		return registry.Result{}, err
	}

	var changes []registry.ResourceChange
	if changesBefore[request.Operation] {
		preview, previewErr := a.Preview(ctx, request)
		if previewErr != nil {
			return registry.Failure(request.OperationID, previewErr), nil
		}
		changes = preview.Changes
	}

	response, err := a.send(ctx, capability, path, request)
	if err != nil {
		return registry.Failure(request.OperationID, err), nil
	}
	if response.StatusCode >= 400 {
		return registry.Failure(request.OperationID,
			upstreamError(response.StatusCode, request.OperationID)).
			FromUpstream(response.StatusCode), nil
	}

	payload, err := unwrap(response.Body, request.OperationID)
	if err != nil {
		return registry.Failure(request.OperationID, err).
			FromUpstream(response.StatusCode), nil
	}
	data, err := registry.Redact(payload, capability)
	if err != nil {
		return registry.Failure(request.OperationID, err), nil
	}
	return registry.Success(request.OperationID, data, changes), nil
}

func (a *Adapter) executableCapability(
	request ExecuteRequest,
) (registry.Capability, error) {
	capability, found := a.byOperation[request.Operation]
	if !found {
		return registry.Capability{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("cloudflare 没有声明操作 " + request.Operation).
			WithOperationID(request.OperationID)
	}
	if !executable[request.Operation] {
		return registry.Capability{}, apperr.New(apperr.CodeNotImplemented).
			WithDetail(request.Operation + " 本期只声明风险等级，未实现执行").
			WithOperationID(request.OperationID)
	}
	return capability, nil
}

// readCurrentRecord 走**被声明过的**单条查询端点取当前值。
//
// 不自己拼一条路径：那样 Preview 就成了绕过端点白名单的入口。
func (a *Adapter) readCurrentRecord(
	ctx context.Context, request ExecuteRequest,
) (map[string]any, error) {
	capability := a.byOperation[OpReadDNSRecord]

	path, err := buildPath(capability, request.Resource, request.OperationID)
	if err != nil {
		return nil, err
	}

	response, err := a.send(ctx, capability, path, ExecuteRequest{
		Operation:   OpReadDNSRecord,
		Resource:    request.Resource,
		Credential:  request.Credential,
		OperationID: request.OperationID,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, upstreamError(response.StatusCode, request.OperationID)
	}

	var envelope struct {
		Result map[string]any `json:"result"`
	}
	if err = json.Unmarshal(response.Body, &envelope); err != nil || envelope.Result == nil {
		return nil, apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail("查不到这条 DNS 记录的当前值，改动不予执行").
			WithOperationID(request.OperationID)
	}
	return envelope.Result, nil
}

func (a *Adapter) send(
	ctx context.Context, capability registry.Capability, path string, request ExecuteRequest,
) (registry.Response, error) {
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
	return a.client.Do(ctx, outbound)
}

// unwrap 取出 Cloudflare 信封里的 result。
//
// Cloudflare 把一切包在 {success, errors, messages, result} 里。不拆开的话，
// 响应过滤白名单只能命中 result 这一个顶层字段，等于整个记录原样放行 ——
// 白名单就白设了。
func unwrap(body []byte, operationID string) ([]byte, error) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("Cloudflare 的响应不是合法 JSON，已放弃返回").
			WithOperationID(operationID)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, apperr.New(apperr.CodeInternal).
			WithDetail("Cloudflare 的响应里没有 result，已放弃返回").
			WithOperationID(operationID)
	}
	return envelope.Result, nil
}

// affectedRecords 报告本次会影响多少条记录（AC3）。
//
// 清缓存是唯一一个「一次请求可以打到很多东西」的 MVP 操作：
// purge_everything 影响范围无法枚举，按不知道处理。
func affectedRecords(capability registry.Capability, input json.RawMessage) int {
	if !capability.Write() {
		return 1
	}
	if capability.Operation != OpPurgeCache {
		return 1
	}

	var purge struct {
		Files           []string `json:"files"`
		PurgeEverything bool     `json:"purge_everything"`
	}
	if err := json.Unmarshal(input, &purge); err != nil || purge.PurgeEverything {
		return UnknownRecordCount
	}
	return len(purge.Files)
}

// compare 把当前值与将要写入的值对成一组变化。
//
// 删除没有新值，after 留空 —— 审批页面据此显示「这条记录会消失」。
func compare(
	operation string, current map[string]any, input json.RawMessage,
) []registry.ResourceChange {
	desired := map[string]any{}
	if operation != OpDeleteDNSRecord {
		if err := json.Unmarshal(input, &desired); err != nil {
			desired = map[string]any{}
		}
	}

	resource := text(current["name"])
	changes := make([]registry.ResourceChange, 0, len(dnsComparedFields))

	for _, field := range dnsComparedFields {
		before := text(current[field])
		after := ""
		if operation != OpDeleteDNSRecord {
			value, present := desired[field]
			if !present {
				continue
			}
			after = text(value)
		}
		if before == "" && after == "" {
			continue
		}
		changes = append(changes, registry.ResourceChange{
			Resource: resource, Field: field, Before: before, After: after,
		})
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
	return changes
}

// dnsComparedFields 是审批页面要对照的字段。
//
// 只对照会改变解析结果的那几项：id 与 zone_id 改不了，展示它们只会让
// 审批页面变长而不是变清楚。
var dnsComparedFields = []string{"type", "name", "content", "ttl", "proxied"}

func text(value any) string {
	switch shaped := value.(type) {
	case nil:
		return ""
	case string:
		return shaped
	case bool:
		return strconv.FormatBool(shaped)
	case float64:
		return strconv.FormatFloat(shaped, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(shaped)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
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

// upstreamError 把 Cloudflare 的状态码折成对外的错误码，不带原始报文。
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
		WithDetail("Cloudflare 返回了一个失败状态").
		WithOperationID(operationID)
}
