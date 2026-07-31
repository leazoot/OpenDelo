package agentauth

import "time"

// AgentType 是 Agent 的接入类型（REQ-AGENT-003）。
//
// 只用于展示与接入方式选择。**它不是身份凭证**：篡改类型不改变身份校验结果，
// 后者由可执行文件哈希与 Session Key 决定。
type AgentType string

// REQ-AGENT-003 声明的五种类型，取值封闭。
const (
	TypeClaudeCode AgentType = "claude-code"
	TypeCodex      AgentType = "codex"
	TypeGeminiCLI  AgentType = "gemini-cli"
	TypeOpenCode   AgentType = "opencode"
	TypeGeneric    AgentType = "generic"
)

// TrustLevel 是 Agent 的信任等级（REQ-AGENT-002），Risk Engine 的输入因子之一。
type TrustLevel string

const (
	// TrustUnverified 是新注册 Agent 的等级（REQ-AGENT-002 AC1）。
	// 这一等级的写操作不得被自动放行（AC2）。
	TrustUnverified TrustLevel = "unverified"
	// TrustKnown 由用户在 Identities 页面确认后取得（AC3）。
	TrustKnown TrustLevel = "known"
	// TrustTrusted 保留给后续版本（REQ-AGENT-002 `[假设]`），本期不会被写入。
	TrustTrusted TrustLevel = "trusted"
)

// AgentStatus 描述 Agent 进程当前是否还在。
type AgentStatus string

const (
	// StatusActive 表示进程在跑且 Session Key 仍在有效期内。
	StatusActive AgentStatus = "active"
	// StatusDisconnected 表示进程已退出或会话已失效。记录保留，因为审计要能追溯到它。
	StatusDisconnected AgentStatus = "disconnected"
)

// DeviceTrust 是设备的信任状态，Risk Engine 的输入因子之一（REQ-RISK-001）。
type DeviceTrust string

const (
	// DeviceTrusted 表示用户确认过这台设备。
	DeviceTrusted DeviceTrust = "trusted"
	// DeviceUntrusted 表示尚未确认或已被取消信任。取消信任会使相关 Trust Memory
	// 失效（REQ-TRUST-004）。
	DeviceUntrusted DeviceTrust = "untrusted"
)

// Device 是请求来源的机器。
type Device struct {
	ID string
	// Fingerprint 跨重启稳定，是「还是上次那台机器吗」的判据。
	Fingerprint string
	Name        string
	TrustStatus DeviceTrust
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Workspace 是请求所属的项目目录。
type Workspace struct {
	ID string
	// Path 是规范化后的绝对路径。
	Path string
	// ProjectFingerprint 变化意味着项目已不是当初被授权的那个，
	// 基于它的绑定必须重新确认（REQ-IDENT-003 AC3）。
	ProjectFingerprint string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Agent 是发起能力请求的执行主体（PRD §9.1）。
//
// ExecutableHash 到 SessionKeyHash 这九项构成 REQ-AGENT-001 的身份绑定，注册后均非空
// （ParentPID 在无父进程时为 0）。Name 与 Type 只用于展示，不参与身份判定。
type Agent struct {
	ID   string
	Name string
	Type AgentType
	// Version 为空表示 Agent 未上报版本，落库为 NULL。
	Version string

	ExecutableHash string
	ExecutablePath string
	PID            int
	ParentPID      int
	OSUser         string
	DeviceID       string
	WorkspaceID    string
	StartedAt      time.Time
	// SessionKeyHash 是 Session Key 的哈希。Session Key 本身不入库，也不在
	// 领域对象中出现 —— 校验只需要比对，不需要还原。
	SessionKeyHash string
	// SessionExpiresAt 之后 Session Key 失效，且不自动续期（REQ-AGENT-001）。
	SessionExpiresAt time.Time

	TrustLevel TrustLevel
	Status     AgentStatus
	LastSeenAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}
