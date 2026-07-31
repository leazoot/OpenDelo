package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * Agent 会话的两端：注册与断开（REQ-API-002 的补充端点，服务 REQ-CLI-002 AC3）。
 *
 * 这两个端点**只服务 `opendelo run`**，因此和其余配置面一样对 Agent 一律 403。
 * 这一条是本设计成立的前提：九项身份绑定必须由一个可信的父进程如实提交，
 * 而按威胁模型 Agent 自己不可信 —— 让它自报 PID 与可执行文件哈希，
 * 等于把 REQ-AGENT-001 AC1「不得仅凭名称识别」变成一句空话。
 *
 * 注册的响应里有一次明文 Session Key。它不是外部服务的凭据（那种东西永远不出缝），
 * 而是 Agent 自己的身份凭证：拿着它只能发起请求，每一次仍要走完决策链路。
 * 它只在这一个响应里出现一次，库里存的是哈希。
 */

// registerAgentBody 是 POST /v1/agents/register 的请求体（REQ-AGENT-001 的输入）。
//
// 九项绑定全部由调用方如实提交。缺任一项时 core 返回
// agent_identity_unverifiable —— 认不出这是谁就不签发会话。
type registerAgentBody struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Version string `json:"version"`

	ExecutableHash string `json:"executable_hash"`
	ExecutablePath string `json:"executable_path"`
	PID            int    `json:"pid"`
	ParentPID      int    `json:"parent_pid"`
	OSUser         string `json:"os_user"`

	DeviceFingerprint string `json:"device_fingerprint"`
	DeviceName        string `json:"device_name"`

	WorkspacePath      string `json:"workspace_path"`
	ProjectFingerprint string `json:"project_fingerprint"`

	// StartedAt 是进程启动时刻，RFC3339。
	StartedAt string `json:"started_at"`
}

// RegisteredAgentView 是 POST /v1/agents/register 的响应。
//
// SessionKey 是明文，且是全部响应里唯一一处明文凭证。调用方拿到之后交给子进程，
// 此后 OpenDelo 自己也取不回来 —— 库里只有哈希。
type RegisteredAgentView struct {
	Agent AgentView `json:"agent"`
	// SessionKey 是本次会话的凭证。
	SessionKey string `json:"session_key"`
	// IsRebind 为真表示命中了同一身份的已有记录（Agent 重启），
	// 绑定其上的授权记忆继续有效。
	IsRebind bool `json:"is_rebind"`
	// ProjectFingerprintChanged 为真表示工作区还是那个路径但项目变了，
	// 依赖该项目的绑定需要重新确认（REQ-IDENT-003 AC3）。
	ProjectFingerprintChanged bool `json:"project_fingerprint_changed"`
}

// SessionRevocationView 是 POST /v1/agents/:id/disconnect 的响应。
type SessionRevocationView struct {
	Agent AgentView `json:"agent"`
	// RevokedLeases 是这次一并收回的会话绑定 Lease 条数。
	RevokedLeases int `json:"revoked_leases"`
}

// registerAgent 注册一个 Agent 并签发 Session Key（REQ-AGENT-001）。
func (e *endpoints) registerAgent(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "Agent 注册"); err != nil {
		return
	}

	var body registerAgentBody
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}

	startedAt, err := body.startedAt()
	if err != nil {
		e.fail(w, r, err)
		return
	}

	registered, err := e.services.AgentAuth.Register(r.Context(), agentauth.Registration{
		Name:               strings.TrimSpace(body.Name),
		Type:               agentauth.AgentType(strings.TrimSpace(body.Type)),
		Version:            strings.TrimSpace(body.Version),
		ExecutableHash:     strings.TrimSpace(body.ExecutableHash),
		ExecutablePath:     body.ExecutablePath,
		PID:                body.PID,
		ParentPID:          body.ParentPID,
		OSUser:             strings.TrimSpace(body.OSUser),
		DeviceFingerprint:  strings.TrimSpace(body.DeviceFingerprint),
		DeviceName:         strings.TrimSpace(body.DeviceName),
		WorkspacePath:      body.WorkspacePath,
		ProjectFingerprint: strings.TrimSpace(body.ProjectFingerprint),
		StartedAt:          startedAt,
	})
	if err != nil {
		e.fail(w, r, err)
		return
	}

	// Reveal 是取出明文的唯一路径，写在这里使这次有意的交付在 review 中可见。
	writeJSON(w, r, e.logger, http.StatusCreated, RegisteredAgentView{
		Agent:                     agentView(registered.Agent),
		SessionKey:                registered.SessionKey.Reveal(),
		IsRebind:                  registered.IsRebind,
		ProjectFingerprintChanged: registered.ProjectFingerprintChanged,
	})
}

// startedAt 解析进程启动时刻。
//
// 缺失与格式错误分开报：前者是「没给」，后者是「给了但不是时间」，
// 调用方要做的事不一样。
func (b registerAgentBody) startedAt() (time.Time, error) {
	raw := strings.TrimSpace(b.StartedAt)
	if raw == "" {
		return time.Time{}, invalidField("started_at", "缺少必填字段 started_at")
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, invalidField("started_at", "started_at 不是 RFC3339 时间")
	}
	return parsed.UTC(), nil
}

// disconnectAgent 结束一个 Agent 会话并收回它名下绑定会话的 Lease（REQ-CLI-002 AC3）。
//
// 幂等：已断开的会话再断一次返回同样的结果，不产生第二次副作用。
func (e *endpoints) disconnectAgent(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "Agent 注册"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	revocation, err := e.services.Pipeline.DisconnectAgent(
		r.Context(), id, logging.OperationIDFrom(r.Context()))
	if err != nil {
		e.fail(w, r, err)
		return
	}

	writeJSON(w, r, e.logger, http.StatusOK, SessionRevocationView{
		Agent:         agentView(revocation.Agent),
		RevokedLeases: len(revocation.RevokedLeases),
	})
}
