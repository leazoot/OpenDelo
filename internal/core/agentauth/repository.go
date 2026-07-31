package agentauth

import (
	"context"
	"time"
)

/*
 * 仓储接口定义在使用方一侧、实现在 internal/store/repo（依赖倒置，
 * 依赖倒置）。core 因此不依赖 SQLite，也不需要为了测试
 * 决策逻辑而准备一个数据库。
 *
 * 三个接口都只暴露本产品真正需要的操作：没有通用的 List/Delete，因为身份记录被
 * 审计引用，删除会让审计断链（外键为 RESTRICT）。
 */

// DeviceRepository 存取请求来源的设备。
type DeviceRepository interface {
	// CreateDevice 写入一台新设备。指纹重复时返回错误，不覆盖已有记录。
	CreateDevice(ctx context.Context, device Device) (Device, error)
	// DeviceByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	DeviceByID(ctx context.Context, id string) (Device, error)
	// DeviceByFingerprint 按设备指纹读取，用于注册时找回已有记录。
	// 不存在时返回 apperr.CodeNotFound。
	DeviceByFingerprint(ctx context.Context, fingerprint string) (Device, error)
	// SetDeviceTrustStatus 改变设备信任状态并把 updatedAt 记为 at。
	SetDeviceTrustStatus(ctx context.Context, id string, status DeviceTrust, at time.Time) (Device, error)
}

// WorkspaceRepository 存取请求所属的项目目录。
type WorkspaceRepository interface {
	// CreateWorkspace 写入一个新工作区。路径重复时返回错误。
	CreateWorkspace(ctx context.Context, workspace Workspace) (Workspace, error)
	// WorkspaceByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	WorkspaceByID(ctx context.Context, id string) (Workspace, error)
	// WorkspaceByPath 按规范化后的绝对路径读取。不存在时返回 apperr.CodeNotFound。
	WorkspaceByPath(ctx context.Context, path string) (Workspace, error)
	// SetWorkspaceFingerprint 更新项目指纹。指纹变化会使基于该项目的绑定失效
	// （REQ-IDENT-003 AC3），失效判定由调用方完成。
	SetWorkspaceFingerprint(ctx context.Context, id, fingerprint string, at time.Time) (Workspace, error)
}

// Binding 是同一个 Agent 跨重启保持不变的那部分身份。
//
// PID 与启动时间每次重启都变，所以不在其中；可执行文件哈希在其中，
// 哈希一变就匹配不到旧记录，注册因此产生一条新的 Agent（REQ-AGENT-001 AC2）。
type Binding struct {
	DeviceID       string
	WorkspaceID    string
	ExecutablePath string
	OSUser         string
	ExecutableHash string
}

// Rebind 是重启后要重新写入的那部分绑定。
type Rebind struct {
	PID              int
	ParentPID        int
	StartedAt        time.Time
	SessionKeyHash   string
	SessionExpiresAt time.Time
	// At 同时写入 last_seen_at 与 updated_at。
	At time.Time
}

// AgentRepository 存取 Agent 身份记录。
type AgentRepository interface {
	// Agents 列出全部 Agent，最近出现的在前，服务 Identities 页面的 Agents 列。
	// limit 必须为正。
	Agents(ctx context.Context, limit int) ([]Agent, error)
	// CreateAgent 写入一条注册记录。设备或工作区不存在时返回错误（外键约束），
	// Session Key 哈希重复时同样返回错误。
	CreateAgent(ctx context.Context, agent Agent) (Agent, error)
	// AgentByBinding 找出上一次以同一身份注册的记录，不存在时返回
	// apperr.CodeNotFound。存在多条时返回最近的一条。
	AgentByBinding(ctx context.Context, binding Binding) (Agent, error)
	// RebindAgent 把重启后的进程上下文与新的 Session Key 写回同一条记录，
	// 并把状态置为 active。旧 Session Key 由此立即失效（REQ-AGENT-001 行为要求 3）。
	RebindAgent(ctx context.Context, id string, rebind Rebind) (Agent, error)
	// AgentByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	AgentByID(ctx context.Context, id string) (Agent, error)
	// AgentBySessionKeyHash 按 Session Key 的哈希读取，是校验 Agent 请求的入口。
	// 不存在时返回 apperr.CodeNotFound —— 调用方据此拒绝，不得回退到按名字识别。
	AgentBySessionKeyHash(ctx context.Context, hash string) (Agent, error)
	// SetAgentTrustLevel 改变信任等级（REQ-AGENT-002 AC3）。
	SetAgentTrustLevel(ctx context.Context, id string, level TrustLevel, at time.Time) (Agent, error)
	// SetAgentStatus 改变在线状态。
	SetAgentStatus(ctx context.Context, id string, status AgentStatus, at time.Time) (Agent, error)
	// TouchAgent 记录最近一次活动时间。
	TouchAgent(ctx context.Context, id string, seenAt time.Time) (Agent, error)
}
