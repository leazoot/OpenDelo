package fixtures

import (
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
)

// DeclarationOption 调整 Adapter 声明夹具。
type DeclarationOption func(*adapters.Declaration)

// WithDeclarationID 换掉声明主键。
func WithDeclarationID(id string) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.ID = id }
}

// WithDeclarationService 换掉服务名。
func WithDeclarationService(service string) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.Service = service }
}

// WithDeclarationKind 换掉 Adapter 种类。
func WithDeclarationKind(kind adapters.Kind) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.Kind = kind }
}

// WithDeclarationAuthScheme 换掉认证形式。
func WithDeclarationAuthScheme(scheme adapters.AuthScheme) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.AuthScheme = scheme }
}

// WithDeclarationRiskLabel 换掉兜底风险标签。
func WithDeclarationRiskLabel(label adapters.RiskLabel) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.DefaultRiskLevel = label }
}

// WithDeclarationCapabilities 换掉能力声明文本，用于验证 JSON 约束。
func WithDeclarationCapabilities(capabilities string) DeclarationOption {
	return func(declaration *adapters.Declaration) { declaration.Capabilities = capabilities }
}

// Declaration 构造一份合法的 Adapter 声明。
func Declaration(options ...DeclarationOption) adapters.Declaration {
	declaration := adapters.Declaration{
		ID:               DefaultAdapterID,
		Service:          DefaultServiceLabel,
		Kind:             adapters.KindGitHub,
		DisplayName:      "GitHub",
		BaseURL:          "https://api.github.com",
		AuthScheme:       adapters.AuthBearer,
		Capabilities:     GitHubCapabilities,
		AllowedPaths:     `["/repos/*"]`,
		AllowedMethods:   `["POST"]`,
		RedactionRules:   `["authorization","token"]`,
		DefaultRiskLevel: adapters.RiskLabelLow,
		Status:           adapters.StatusEnabled,
		CreatedAt:        Instant,
		UpdatedAt:        Instant,
	}
	for _, apply := range options {
		apply(&declaration)
	}
	return declaration
}

// RequestOption 调整能力请求夹具。
type RequestOption func(*pipeline.CapabilityRequest)

// WithRequestID 换掉请求主键。
func WithRequestID(id string) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.ID = id }
}

// WithRequestAgentID 换掉发起请求的 Agent。
func WithRequestAgentID(agentID string) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.AgentID = agentID }
}

// WithRequestWorkspaceID 换掉请求所在的工作区。
func WithRequestWorkspaceID(workspaceID string) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.WorkspaceID = workspaceID }
}

// WithRequestStatus 换掉请求状态。
func WithRequestStatus(status pipeline.RequestStatus) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.Status = status }
}

// WithRequestResource 换掉资源文本，用于验证 JSON 约束。
func WithRequestResource(resource string) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.Resource = resource }
}

// WithRequestDesiredChange 换掉期望变更文本。空串表示读操作。
func WithRequestDesiredChange(change string) RequestOption {
	return func(request *pipeline.CapabilityRequest) { request.DesiredChange = change }
}

// Request 构造一条合法的能力请求。
//
// 默认是写操作：读操作用 WithRequestDesiredChange("") 得到。
func Request(options ...RequestOption) pipeline.CapabilityRequest {
	request := pipeline.CapabilityRequest{
		ID:            DefaultRequestID,
		OperationID:   "01K1OPERATION00000000000000",
		AgentID:       DefaultAgentID,
		WorkspaceID:   DefaultWorkspaceID,
		Service:       DefaultServiceLabel,
		Operation:     "pull_request.create",
		Resource:      `{"repo":"Runcoor/opendelo"}`,
		DesiredChange: `{"base":"main","head":"feature"}`,
		Reason:        "Open the release pull request",
		Status:        pipeline.StatusReceived,
		CreatedAt:     Instant,
		UpdatedAt:     Instant,
	}
	for _, apply := range options {
		apply(&request)
	}
	return request
}

// DecisionOption 调整决策夹具。
type DecisionOption func(*decision.Decision)

// WithDecisionID 换掉决策主键。
func WithDecisionID(id string) DecisionOption {
	return func(record *decision.Decision) { record.ID = id }
}

// WithDecisionRequestID 换掉决策指向的能力请求。
func WithDecisionRequestID(requestID string) DecisionOption {
	return func(record *decision.Decision) { record.CapabilityRequestID = requestID }
}

// WithDecisionVerdict 换掉结论。
func WithDecisionVerdict(verdict decision.Verdict) DecisionOption {
	return func(record *decision.Decision) { record.Verdict = verdict }
}

// WithDecisionRiskLevel 换掉风险等级。
func WithDecisionRiskLevel(level risk.Level) DecisionOption {
	return func(record *decision.Decision) { record.RiskLevel = level }
}

// WithDecisionApprovalRequirement 换掉确认强度。
func WithDecisionApprovalRequirement(requirement decision.ApprovalRequirement) DecisionOption {
	return func(record *decision.Decision) { record.ApprovalRequirement = requirement }
}

// WithDecisionMatch 同时换掉匹配到的身份与命中层级。二者必须同进同出。
func WithDecisionMatch(identityID string, level matcher.MatchLevel) DecisionOption {
	return func(record *decision.Decision) {
		record.IdentityID = identityID
		record.MatchLevel = level
	}
}

// WithDecisionIdentityID 只换身份，不动命中层级，用于验证「同进同出」约束。
func WithDecisionIdentityID(identityID string) DecisionOption {
	return func(record *decision.Decision) { record.IdentityID = identityID }
}

// WithDecisionMatchLevel 只换命中层级，不动身份，用于验证「同进同出」约束。
func WithDecisionMatchLevel(level matcher.MatchLevel) DecisionOption {
	return func(record *decision.Decision) { record.MatchLevel = level }
}

// WithDecisionRiskFactors 换掉风险因子文本，用于验证 JSON 约束。
func WithDecisionRiskFactors(factors string) DecisionOption {
	return func(record *decision.Decision) { record.RiskFactors = factors }
}

// WithDecisionResolvedScope 换掉收敛后的范围，用于验证签发时读回的正是它。
func WithDecisionResolvedScope(resolved string) DecisionOption {
	return func(record *decision.Decision) { record.ResolvedScope = resolved }
}

// Decision 构造一条合法的决策记录。
func Decision(options ...DecisionOption) decision.Decision {
	record := decision.Decision{
		ID:                  DefaultDecisionID,
		CapabilityRequestID: DefaultRequestID,
		Verdict:             decision.VerdictRequireApproval,
		RiskLevel:           risk.LevelMedium,
		RiskFactors:         `["write","production"]`,
		IdentityID:          DefaultIdentityID,
		MatchLevel:          matcher.MatchWorkspaceBinding,
		ResolvedScope:       `{"service":"github","repo":"Runcoor/opendelo"}`,
		ApprovalRequirement: decision.ApprovalStandard,
		ReasonCode:          "risk_requires_confirmation",
		CreatedAt:           Instant,
	}
	for _, apply := range options {
		apply(&record)
	}
	return record
}

// 能力声明文本。字段集合由 internal/core/intent 定义并校验：工具名、操作名、
// 方法、路径、风险标签，以及是否幂等 / 可逆 / 涉及敏感数据与构成资源标识的字段。
// 三个布尔值都必须显式出现 —— 「没声明」与「声明为 false」在决策链路里不是一回事。
const (
	// GitHubCapabilities 与 Request 夹具描述的是同一次调用。
	GitHubCapabilities = `[{"tool":"github.pull_request.create",` +
		`"operation":"pull_request.create","method":"POST",` +
		`"path":"/repos/{owner}/{repo}/pulls","risk":"medium",` +
		`"idempotent":false,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["repo"]}]`

	// CloudflareCapabilities 是 REQ-INTENT-001 AC1 点名的那条 DNS 声明。
	CloudflareCapabilities = `[{"tool":"cloudflare.dns.update",` +
		`"operation":"dns.record.update","method":"PUT",` +
		`"path":"/zones/{zone_id}/dns_records/{record_id}","risk":"medium",` +
		`"idempotent":true,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["zone","record"]}]`

	// CloudflareRecordCapabilities 是 REQ-SCOPE-001 AC1 点名的那条声明：
	// 收敛出的 Scope 要精确到 zone + record + record_type，所以三个字段都要
	// 出现在资源标识里。
	CloudflareRecordCapabilities = `[{"tool":"cloudflare.dns.update",` +
		`"operation":"dns.record.update","method":"PUT",` +
		`"path":"/zones/{zone_id}/dns_records/{record_id}","risk":"medium",` +
		`"idempotent":true,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["zone","record","record_type"]}]`
)

// CloudflareDeclaration 构造 REQ-INTENT-001 AC1 用到的 Cloudflare 声明。
func CloudflareDeclaration(options ...DeclarationOption) adapters.Declaration {
	return Declaration(append([]DeclarationOption{
		WithDeclarationID("01K1ADAPTER000000000000002"),
		WithDeclarationService("cloudflare"),
		WithDeclarationKind(adapters.KindCloudflare),
		WithDeclarationCapabilities(CloudflareCapabilities),
	}, options...)...)
}
