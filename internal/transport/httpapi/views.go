package httpapi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/settings"
)

/*
 * 响应视图。
 *
 * 领域对象与数据库行不直接序列化：视图里出现什么字段是一个要被审查的决定，
 * 而不是「结构体加了个字段就顺带发出去了」的后果。
 *
 * 这里没有任何字段能表达凭据。不是「记得别填」，是**填不进去** ——
 * 凭据只以 platform/secret.Value 流转，而那个类型在本包不可见（架构测试强制）。
 */

// CapabilityRequestView 是一次能力请求对外的样子（REQ-CAP-002 AC1）。
type CapabilityRequestView struct {
	ID          string `json:"id"`
	OperationID string `json:"operation_id"`
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	Service     string `json:"service"`
	Operation   string `json:"operation"`
	// Resource 与 DesiredChange 是请求里原样带来的 JSON。Adapter 的响应正文
	// 不出现在这里 —— 那条路要先过 registry.Redact。
	Resource      json.RawMessage `json:"resource"`
	DesiredChange json.RawMessage `json:"desired_change"`
	// ChangePreview 是执行前查出来的旧值（REQ-APPROVAL-001 AC4）。
	// null 表示没有查过 —— 与「查过但没有可对照的字段」是两句不同的话。
	// 它经 Adapter 的字段白名单来，不含凭据。
	ChangePreview json.RawMessage `json:"change_preview"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	// WithheldOperations 是这个服务里没有被这次请求覆盖的操作。
	// 卷宗据此说清放行的边界，而不是让界面自己编三句否定句。
	WithheldOperations []string `json:"withheld_operations"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	// Decision 在决策已经写下时非空。
	Decision *DecisionView `json:"decision"`
}

// DecisionView 是一次决策对外的样子。
//
// 带上 RiskFactors：Access Folio 要能回答「为什么是这个等级」（REQ-RISK-001 AC3），
// 没有它就只剩一个无从解释的标签。
type DecisionView struct {
	ID                  string          `json:"id"`
	Verdict             string          `json:"verdict"`
	RiskLevel           string          `json:"risk_level"`
	RiskFactors         json.RawMessage `json:"risk_factors"`
	IdentityID          string          `json:"identity_id"`
	MatchLevel          string          `json:"match_level"`
	ResolvedScope       json.RawMessage `json:"resolved_scope"`
	ApprovalRequirement string          `json:"approval_requirement"`
	ReasonCode          string          `json:"reason_code"`
	TrustMemoryID       string          `json:"trust_memory_id"`
	CreatedAt           string          `json:"created_at"`
}

// ApprovalView 是一个审批项对外的样子。
type ApprovalView struct {
	ID         string `json:"id"`
	DecisionID string `json:"decision_id"`
	Status     string `json:"status"`
	Action     string `json:"action"`
	ExpiresAt  string `json:"expires_at"`
	DecidedAt  string `json:"decided_at"`
	CreatedAt  string `json:"created_at"`
	// AvailableActions 是这个风险等级下允许提供的操作（REQ-APPROVAL-002）。
	// 由后端给出而不是让界面自己推：高风险不提供学习类操作这条规则
	// 只能有一个答案，界面照着渲染即可。
	AvailableActions []string `json:"available_actions"`
	// Request 与 Decision 让 Gate 页面一次拿齐渲染 Arrival 所需的内容。
	Request  *CapabilityRequestView `json:"request"`
	Decision *DecisionView          `json:"decision"`
}

// LeaseView 是一条 Lease 对外的样子。
type LeaseView struct {
	ID            string          `json:"id"`
	AgentID       string          `json:"agent_id"`
	IdentityID    string          `json:"identity_id"`
	Service       string          `json:"service"`
	ResourceScope json.RawMessage `json:"resource_scope"`
	Capabilities  json.RawMessage `json:"capabilities"`
	ExpiresAt     string          `json:"expires_at"`
	// RequestLimit 为空表示不限次数。用指针而不是 0：界面上「不限」与
	// 「还剩 0 次」是两句完全不同的话。
	RequestLimit   *int   `json:"request_limit"`
	UsedRequests   int    `json:"used_requests"`
	Status         string `json:"status"`
	ApprovalID     string `json:"approval_id"`
	SourceMemoryID string `json:"source_memory_id"`
	IsSessionBound bool   `json:"is_session_bound"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// listEnvelope 是列表响应的统一形状。
type listEnvelope[T any] struct {
	Items []T `json:"items"`
	// NextCursor 为空表示没有下一页。
	NextCursor string `json:"next_cursor"`
}

// settlementEnvelope 是审批决策端点的响应。
type settlementEnvelope struct {
	Approval ApprovalView           `json:"approval"`
	Request  *CapabilityRequestView `json:"request"`
	// Lease 只在放行时非空；重复提交时是首次签发的那一条（REQ-API-004）。
	Lease *LeaseView `json:"lease"`
	// TrustMemoryID 在这次审批学到了一条记忆时非空。
	TrustMemoryID string `json:"trust_memory_id"`
	// Replayed 为真表示这次提交重放了之前的同一个决定，没有产生新的后果。
	Replayed bool `json:"replayed"`
}

func requestView(
	request pipeline.CapabilityRequest, record *DecisionView, withheld []string,
) CapabilityRequestView {
	return CapabilityRequestView{
		ID:                 request.ID,
		OperationID:        request.OperationID,
		AgentID:            request.AgentID,
		WorkspaceID:        request.WorkspaceID,
		Service:            request.Service,
		Operation:          request.Operation,
		Resource:           rawJSON(request.Resource),
		DesiredChange:      rawJSON(request.DesiredChange),
		ChangePreview:      rawJSON(request.ChangePreview),
		Reason:             request.Reason,
		Status:             string(request.Status),
		WithheldOperations: withheld,
		CreatedAt:          formatTime(request.CreatedAt),
		UpdatedAt:          formatTime(request.UpdatedAt),
		Decision:           record,
	}
}

// withheldOperations 是这个服务里**没有**被这次请求覆盖的操作（REQ-APPROVAL-001 AC4）。
//
// 从能力声明里减去这一个，而不是列一份写死的否定句：Adapter 增删操作时这段话
// 会跟着变，而写死的那三句不会。声明表里认不出这个服务时返回空切片 ——
// 那时一条也说不出来，编出来的否定句比不说更糟。
func (e *endpoints) withheldOperations(service, granted string) []string {
	declared := e.services.Capabilities.Operations(service)
	withheld := make([]string, 0, len(declared))
	for _, operation := range declared {
		if operation != granted {
			withheld = append(withheld, operation)
		}
	}
	return withheld
}

// hideStrongAuth 对 Agent 隐去「这次放行要不要当面认证」（REQ-APPROVAL-005 AC3）。
//
// Agent 只需要知道自己在等人；知道人还要过一次 Passkey，就多了一条
// 「什么时候有人正站在设备前」的线索。verdict 已经说清了它该做什么，
// 这一项对它没有任何用途。
func hideStrongAuth(view *DecisionView) {
	if view != nil {
		view.ApprovalRequirement = ""
	}
}

func decisionView(record decision.Decision) DecisionView {
	return DecisionView{
		ID:                  record.ID,
		Verdict:             string(record.Verdict),
		RiskLevel:           string(record.RiskLevel),
		RiskFactors:         rawJSON(record.RiskFactors),
		IdentityID:          record.IdentityID,
		MatchLevel:          string(record.MatchLevel),
		ResolvedScope:       rawJSON(record.ResolvedScope),
		ApprovalRequirement: string(record.ApprovalRequirement),
		ReasonCode:          record.ReasonCode,
		TrustMemoryID:       record.TrustMemoryID,
		CreatedAt:           formatTime(record.CreatedAt),
	}
}

func approvalView(
	item approval.Approval, actions []approval.Action,
	request *CapabilityRequestView, record *DecisionView,
) ApprovalView {
	available := make([]string, 0, len(actions))
	for _, action := range actions {
		available = append(available, string(action))
	}

	return ApprovalView{
		ID:               item.ID,
		DecisionID:       item.DecisionID,
		Status:           string(item.Status),
		Action:           string(item.Action),
		ExpiresAt:        formatTime(item.ExpiresAt),
		DecidedAt:        formatOptionalTime(item.DecidedAt),
		CreatedAt:        formatTime(item.CreatedAt),
		AvailableActions: available,
		Request:          request,
		Decision:         record,
	}
}

func leaseView(issued lease.Lease) LeaseView {
	view := LeaseView{
		ID:             issued.ID,
		AgentID:        issued.AgentID,
		IdentityID:     issued.IdentityID,
		Service:        issued.Service,
		ResourceScope:  rawJSON(issued.ResourceScope),
		Capabilities:   rawJSON(issued.Capabilities),
		ExpiresAt:      formatTime(issued.ExpiresAt),
		UsedRequests:   issued.UsedRequests,
		Status:         string(issued.Status),
		ApprovalID:     issued.ApprovalID,
		SourceMemoryID: issued.SourceMemoryID,
		IsSessionBound: issued.IsSessionBound,
		CreatedAt:      formatTime(issued.CreatedAt),
		UpdatedAt:      formatTime(issued.UpdatedAt),
	}
	if issued.RequestLimit != lease.Unlimited {
		limit := issued.RequestLimit
		view.RequestLimit = &limit
	}
	return view
}

// rawJSON 把库里存的 JSON 文本原样带出去。
//
// 空串换成 JSON 的 null 而不是空对象：「这次请求没有变更」与「变更是个空对象」
// 在审批页面上是两句不同的话（REQ-APPROVAL-001 的 Explain Real Impact）。
func rawJSON(stored string) json.RawMessage {
	if stored == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(stored)
}

// IdentityView 是一个身份对外的样子。
//
// 没有任何字段能表达凭据：只有 `credential_reference_id`，一个指针而不是内容。
// 「已连接的 Token 前四位」这类展示在这里连位置都没有
type IdentityView struct {
	ID                    string `json:"id"`
	Service               string `json:"service"`
	AccountLabel          string `json:"account_label"`
	Environment           string `json:"environment"`
	IsDefault             bool   `json:"is_default"`
	Status                string `json:"status"`
	CredentialReferenceID string `json:"credential_reference_id"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

// verificationEnvelope 是身份验证端点的响应。
type verificationEnvelope struct {
	Identity IdentityView `json:"identity"`
	// Health 是凭据来源这一刻的探测结论：ok / needs_reauth / unavailable。
	Health string `json:"health"`
}

// revocationEnvelope 是身份断开端点的响应。
//
// 只报条数不报明细：Console 需要知道「收回了几条」来给出确认反馈，
// 而逐条明细在账本里 —— 每一条撤销都记了一次 lease.revoked。
type revocationEnvelope struct {
	Identity            IdentityView `json:"identity"`
	RevokedLeases       int          `json:"revoked_leases"`
	InvalidatedMemories int          `json:"invalidated_memories"`
}

// TrustMemoryView 是一条授权记忆对外的样子。
type TrustMemoryView struct {
	ID              string          `json:"id"`
	AgentID         string          `json:"agent_id"`
	WorkspaceID     string          `json:"workspace_id"`
	IdentityID      string          `json:"identity_id"`
	Service         string          `json:"service"`
	ResourceScope   json.RawMessage `json:"resource_scope"`
	CapabilityScope json.RawMessage `json:"capability_scope"`
	Environment     string          `json:"environment"`
	RiskCeiling     string          `json:"risk_ceiling"`
	Behavior        string          `json:"approval_behavior"`
	// CreatedFrom 是产生这条记忆的审批（REQ-TRUST-001 AC2）：
	// Automation 页面据此显示「由你在某次审批中创建」。
	CreatedFrom string `json:"created_from"`
	Status      string `json:"status"`
	// InvalidationReason 在失效时非空。失效的记忆不消失，要说明为什么
	// （REQ-TRUST-004 AC2）。
	InvalidationReason string `json:"invalidation_reason"`
	LastUsedAt         string `json:"last_used_at"`
	ExpiresAt          string `json:"expires_at"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

// AuditEventView 是一条账本记录对外的样子。
//
// 展示与导出共用它（REQ-AUDIT-004 AC2）：脱敏只有一处，不存在
// 「导出那条路忘了做」的可能。三个 JSON 字段在写入账本时就已经递归脱敏过。
type AuditEventView struct {
	ID          string `json:"id"`
	OperationID string `json:"operation_id"`
	Type        string `json:"type"`

	AgentID     string `json:"agent_id"`
	DeviceID    string `json:"device_id"`
	WorkspaceID string `json:"workspace_id"`
	IdentityID  string `json:"identity_id"`

	Service       string          `json:"service"`
	Operation     string          `json:"operation"`
	Resource      json.RawMessage `json:"resource"`
	ResolvedScope json.RawMessage `json:"resolved_scope"`

	Verdict     string `json:"verdict"`
	RiskLevel   string `json:"risk_level"`
	DecisionID  string `json:"decision_id"`
	ApprovalID  string `json:"approval_id"`
	LeaseID     string `json:"lease_id"`
	LeaseStatus string `json:"lease_status"`

	Outcome        string `json:"outcome"`
	ResponseStatus int    `json:"response_status"`
	DurationMillis int64  `json:"duration_ms"`
	IsRedacted     bool   `json:"is_redacted"`

	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt string          `json:"created_at"`
}

// row 是这条记录在 CSV 里的一行，列序与 csvColumns 一致。
func (v AuditEventView) row() []string {
	return []string{
		v.ID, v.OperationID, v.Type,
		v.AgentID, v.DeviceID, v.WorkspaceID, v.IdentityID,
		v.Service, v.Operation, string(v.Resource), string(v.ResolvedScope),
		v.Verdict, v.RiskLevel, v.DecisionID, v.ApprovalID, v.LeaseID,
		v.LeaseStatus, v.Outcome,
		strconv.Itoa(v.ResponseStatus), strconv.FormatInt(v.DurationMillis, 10),
		strconv.FormatBool(v.IsRedacted), string(v.Metadata), v.CreatedAt,
	}
}

func identityView(identity matcher.Identity) IdentityView {
	return IdentityView{
		ID:                    identity.ID,
		Service:               identity.Service,
		AccountLabel:          identity.AccountLabel,
		Environment:           string(identity.Environment),
		IsDefault:             identity.IsDefault,
		Status:                string(identity.Status),
		CredentialReferenceID: identity.CredentialReferenceID,
		CreatedAt:             formatTime(identity.CreatedAt),
		UpdatedAt:             formatTime(identity.UpdatedAt),
	}
}

func memoryView(memory trust.Memory) TrustMemoryView {
	return TrustMemoryView{
		ID:                 memory.ID,
		AgentID:            memory.AgentID,
		WorkspaceID:        memory.WorkspaceID,
		IdentityID:         memory.IdentityID,
		Service:            memory.Service,
		ResourceScope:      rawJSON(memory.ResourceScope),
		CapabilityScope:    rawJSON(memory.CapabilityScope),
		Environment:        string(memory.Environment),
		RiskCeiling:        string(memory.RiskCeiling),
		Behavior:           string(memory.Behavior),
		CreatedFrom:        memory.CreatedFrom,
		Status:             string(memory.Status),
		InvalidationReason: string(memory.InvalidationReason),
		LastUsedAt:         formatOptionalTime(memory.LastUsedAt),
		ExpiresAt:          formatTime(memory.ExpiresAt),
		CreatedAt:          formatTime(memory.CreatedAt),
		UpdatedAt:          formatTime(memory.UpdatedAt),
	}
}

func auditEventView(event audit.Event) AuditEventView {
	return AuditEventView{
		ID:             event.ID,
		OperationID:    event.OperationID,
		Type:           string(event.Type),
		AgentID:        event.AgentID,
		DeviceID:       event.DeviceID,
		WorkspaceID:    event.WorkspaceID,
		IdentityID:     event.IdentityID,
		Service:        event.Service,
		Operation:      event.Operation,
		Resource:       rawJSON(event.Resource),
		ResolvedScope:  rawJSON(event.ResolvedScope),
		Verdict:        string(event.Verdict),
		RiskLevel:      string(event.RiskLevel),
		DecisionID:     event.DecisionID,
		ApprovalID:     event.ApprovalID,
		LeaseID:        event.LeaseID,
		LeaseStatus:    string(event.LeaseStatus),
		Outcome:        string(event.Outcome),
		ResponseStatus: event.ResponseStatus,
		DurationMillis: event.Duration.Milliseconds(),
		IsRedacted:     event.IsRedacted,
		Metadata:       rawJSON(event.Metadata),
		CreatedAt:      formatTime(event.CreatedAt),
	}
}

// AgentView 是一个 Agent 对外的样子。
//
// 没有 session_key_hash：那是校验用的比对材料，界面上没有任何用途，
// 而它一旦出现在响应里就成了一条可以被离线爆破的线索。
type AgentView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	DeviceID    string `json:"device_id"`
	WorkspaceID string `json:"workspace_id"`
	// ExecutablePath 让用户认得出这是哪个进程；哈希只给前 12 位，
	// 够用来分辨「换了一个二进制」，不必把整串摆出来。
	ExecutablePath   string `json:"executable_path"`
	ExecutableDigest string `json:"executable_digest"`
	OSUser           string `json:"os_user"`
	TrustLevel       string `json:"trust_level"`
	Status           string `json:"status"`
	StartedAt        string `json:"started_at"`
	SessionExpiresAt string `json:"session_expires_at"`
	LastSeenAt       string `json:"last_seen_at"`
	CreatedAt        string `json:"created_at"`
}

// PreferencesView 是 GET /v1/preferences 的响应。
//
// 运行期偏好与需要重启的设置分开摆：把它们混成一层，用户就无从知道
// 自己刚才那次修改是立刻生效了还是要重启（REQ-PREF-001 AC1/AC2）。
type PreferencesView struct {
	// AutomationMode 等是改完立刻生效的。
	AutomationMode        string `json:"automation_mode"`
	ApprovalTimeoutSecond int    `json:"approval_timeout_seconds"`
	ReadOnlyAutoAllow     bool   `json:"read_only_auto_allow"`
	Theme                 string `json:"theme"`
	Language              string `json:"language"`
	// RestartRequired 是改完要重启才生效的那些，本端点只读不写。
	RestartRequired RestartRequiredView `json:"restart_required"`
	// Warnings 是读配置时认不出的项（REQ-PREF-001 AC3：用默认值继续并告警）。
	Warnings []string `json:"warnings"`
}

// RestartRequiredView 是那些改完必须重启的设置。
type RestartRequiredView struct {
	ListenAddress  string `json:"listen_address"`
	WebAPIPort     int    `json:"web_api_port"`
	AgentProxyPort int    `json:"agent_proxy_port"`
	MCPPort        int    `json:"mcp_port"`
	LogLevel       string `json:"log_level"`
}

// VaultView 是保险库的锁定状态。
//
// 只有一个布尔值：条目数、引用名、上次解锁时间都不在这里 ——
// 那些合起来就能勾勒出这台机器上存着什么。
type VaultView struct {
	Unlocked bool `json:"unlocked"`
}

func agentView(agent agentauth.Agent) AgentView {
	return AgentView{
		ID:               agent.ID,
		Name:             agent.Name,
		Type:             string(agent.Type),
		Version:          agent.Version,
		DeviceID:         agent.DeviceID,
		WorkspaceID:      agent.WorkspaceID,
		ExecutablePath:   agent.ExecutablePath,
		ExecutableDigest: shortDigest(agent.ExecutableHash),
		OSUser:           agent.OSUser,
		TrustLevel:       string(agent.TrustLevel),
		Status:           string(agent.Status),
		StartedAt:        formatTime(agent.StartedAt),
		SessionExpiresAt: formatTime(agent.SessionExpiresAt),
		LastSeenAt:       formatOptionalTime(agent.LastSeenAt),
		CreatedAt:        formatTime(agent.CreatedAt),
	}
}

// shortDigest 去掉算法前缀后截出 12 位，够分辨「换了一个二进制」。
//
// 去前缀不是为了好看：带上 "sha256:" 之后，12 个字符里只剩 5 位有效摘要，
// 两个不同的二进制会显示成同一串。
func shortDigest(hash string) string {
	const shown = 12

	digest := hash
	if _, rest, found := strings.Cut(hash, ":"); found {
		digest = rest
	}
	if len(digest) <= shown {
		return digest
	}
	return digest[:shown]
}

func preferencesView(
	current settings.Preferences, boot config.Config, warnings []string,
) PreferencesView {
	if warnings == nil {
		warnings = []string{}
	}

	return PreferencesView{
		AutomationMode:        string(current.AutomationMode),
		ApprovalTimeoutSecond: int(current.ApprovalTimeout.Seconds()),
		ReadOnlyAutoAllow:     current.ReadOnlyAutoAllow,
		Theme:                 current.Theme,
		Language:              current.Language,
		RestartRequired: RestartRequiredView{
			ListenAddress:  boot.ListenAddress,
			WebAPIPort:     boot.WebAPIPort,
			AgentProxyPort: boot.AgentProxyPort,
			MCPPort:        boot.MCPPort,
			LogLevel:       boot.LogLevel,
		},
		Warnings: warnings,
	}
}

func vaultView(unlocked bool) VaultView { return VaultView{Unlocked: unlocked} }
