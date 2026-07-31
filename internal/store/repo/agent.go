package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Agents 是 agentauth.AgentRepository 的 SQLite 实现。
type Agents struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ agentauth.AgentRepository = (*Agents)(nil)

// NewAgents 绑定到已迁移的数据库。
func NewAgents(db *store.DB) *Agents {
	return &Agents{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (a *Agents) CreateAgent(ctx context.Context, agent agentauth.Agent) (agentauth.Agent, error) {
	row, err := a.write.CreateAgent(ctx, queries.CreateAgentParams{
		ID:               agent.ID,
		Name:             agent.Name,
		Type:             string(agent.Type),
		Version:          optionalText(agent.Version),
		ExecutableHash:   agent.ExecutableHash,
		ExecutablePath:   agent.ExecutablePath,
		Pid:              int64(agent.PID),
		ParentPid:        int64(agent.ParentPID),
		OsUser:           agent.OSUser,
		DeviceID:         agent.DeviceID,
		WorkspaceID:      agent.WorkspaceID,
		StartedAt:        encodeTime(agent.StartedAt),
		SessionKeyHash:   agent.SessionKeyHash,
		SessionExpiresAt: encodeTime(agent.SessionExpiresAt),
		TrustLevel:       string(agent.TrustLevel),
		Status:           string(agent.Status),
		LastSeenAt:       encodeTime(agent.LastSeenAt),
		CreatedAt:        encodeTime(agent.CreatedAt),
		UpdatedAt:        encodeTime(agent.UpdatedAt),
	})
	if err != nil {
		return agentauth.Agent{}, writeError(err, "写入 Agent "+agent.ID+" 失败")
	}
	return toAgent(row)
}

// Agents 列出全部 Agent，最近出现的在前。
//
// 无界列表查询由这一层拒绝。
func (a *Agents) Agents(ctx context.Context, limit int) ([]agentauth.Agent, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListAgents(ctx, int64(limit))
	if err != nil {
		return nil, readError(err, "列出 Agent 失败")
	}

	agents := make([]agentauth.Agent, 0, len(rows))
	for _, row := range rows {
		agent, convertErr := toAgent(row)
		if convertErr != nil {
			return nil, convertErr
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func (a *Agents) AgentByID(ctx context.Context, id string) (agentauth.Agent, error) {
	row, err := a.read.GetAgentByID(ctx, id)
	if err != nil {
		return agentauth.Agent{}, readError(err, "读取 Agent "+id+" 失败")
	}
	return toAgent(row)
}

func (a *Agents) AgentBySessionKeyHash(ctx context.Context, hash string) (agentauth.Agent, error) {
	row, err := a.read.GetAgentBySessionKeyHash(ctx, hash)
	if err != nil {
		// 哈希不进错误详情：它是会话凭证的派生物，账本与日志里都不该出现。
		return agentauth.Agent{}, readError(err, "按会话密钥读取 Agent 失败")
	}
	return toAgent(row)
}

// AgentByBinding 找出上一次以同一身份注册的记录：设备、工作区、可执行文件路径、
// 系统用户与可执行文件哈希都相同（REQ-AGENT-001 行为要求 3）。哈希变了就匹配不到，
// 注册于是产生一条新记录，旧记忆随旧记录留在原地（AC2）。
//
// 命中多条时取 id 最大的一条：ULID 时间有序，等价于最近一次。
func (a *Agents) AgentByBinding(ctx context.Context, binding agentauth.Binding) (agentauth.Agent, error) {
	row, err := a.read.GetAgentByBinding(ctx, queries.GetAgentByBindingParams{
		DeviceID:       binding.DeviceID,
		WorkspaceID:    binding.WorkspaceID,
		ExecutablePath: binding.ExecutablePath,
		OsUser:         binding.OSUser,
		ExecutableHash: binding.ExecutableHash,
	})
	if err != nil {
		return agentauth.Agent{}, readError(err, "按身份绑定读取 Agent 失败")
	}
	return toAgent(row)
}

// RebindAgent 把重启后的进程上下文与新的 Session Key 写回同一条记录并置为 active。
// 旧 Session Key 因此立即失效，而 Agent 主键不变，绑定其上的授权记忆得以跨重启保留 ——
// 只有可执行文件变化才该让记忆失效（PRD §14.3），重启本身不该。
func (a *Agents) RebindAgent(ctx context.Context, id string, rebind agentauth.Rebind) (agentauth.Agent, error) {
	row, err := a.write.RebindAgent(ctx, queries.RebindAgentParams{
		Pid:              int64(rebind.PID),
		ParentPid:        int64(rebind.ParentPID),
		StartedAt:        encodeTime(rebind.StartedAt),
		SessionKeyHash:   rebind.SessionKeyHash,
		SessionExpiresAt: encodeTime(rebind.SessionExpiresAt),
		LastSeenAt:       encodeTime(rebind.At),
		UpdatedAt:        encodeTime(rebind.At),
		ID:               id,
	})
	if err != nil {
		return agentauth.Agent{}, writeError(err, "重新绑定 Agent "+id+" 失败")
	}
	return toAgent(row)
}

func (a *Agents) SetAgentTrustLevel(
	ctx context.Context, id string, level agentauth.TrustLevel, at time.Time,
) (agentauth.Agent, error) {
	row, err := a.write.UpdateAgentTrustLevel(ctx, queries.UpdateAgentTrustLevelParams{
		TrustLevel: string(level),
		UpdatedAt:  encodeTime(at),
		ID:         id,
	})
	if err != nil {
		return agentauth.Agent{}, writeError(err, "更新 Agent "+id+" 的信任等级失败")
	}
	return toAgent(row)
}

func (a *Agents) SetAgentStatus(
	ctx context.Context, id string, status agentauth.AgentStatus, at time.Time,
) (agentauth.Agent, error) {
	row, err := a.write.UpdateAgentStatus(ctx, queries.UpdateAgentStatusParams{
		Status:    string(status),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if err != nil {
		return agentauth.Agent{}, writeError(err, "更新 Agent "+id+" 的状态失败")
	}
	return toAgent(row)
}

func (a *Agents) TouchAgent(ctx context.Context, id string, seenAt time.Time) (agentauth.Agent, error) {
	row, err := a.write.UpdateAgentLastSeen(ctx, queries.UpdateAgentLastSeenParams{
		LastSeenAt: encodeTime(seenAt),
		UpdatedAt:  encodeTime(seenAt),
		ID:         id,
	})
	if err != nil {
		return agentauth.Agent{}, writeError(err, "更新 Agent "+id+" 的活动时间失败")
	}
	return toAgent(row)
}

func toAgent(row queries.Agent) (agentauth.Agent, error) {
	startedAt, err := decodeTime("agents.started_at", row.StartedAt)
	if err != nil {
		return agentauth.Agent{}, err
	}
	sessionExpiresAt, err := decodeTime("agents.session_expires_at", row.SessionExpiresAt)
	if err != nil {
		return agentauth.Agent{}, err
	}
	lastSeenAt, err := decodeTime("agents.last_seen_at", row.LastSeenAt)
	if err != nil {
		return agentauth.Agent{}, err
	}
	createdAt, err := decodeTime("agents.created_at", row.CreatedAt)
	if err != nil {
		return agentauth.Agent{}, err
	}
	updatedAt, err := decodeTime("agents.updated_at", row.UpdatedAt)
	if err != nil {
		return agentauth.Agent{}, err
	}

	processID, err := decodeProcessID("agents.pid", row.Pid)
	if err != nil {
		return agentauth.Agent{}, err
	}
	parentProcessID, err := decodeProcessID("agents.parent_pid", row.ParentPid)
	if err != nil {
		return agentauth.Agent{}, err
	}

	return agentauth.Agent{
		ID:               row.ID,
		Name:             row.Name,
		Type:             agentauth.AgentType(row.Type),
		Version:          row.Version.String,
		ExecutableHash:   row.ExecutableHash,
		ExecutablePath:   row.ExecutablePath,
		PID:              processID,
		ParentPID:        parentProcessID,
		OSUser:           row.OsUser,
		DeviceID:         row.DeviceID,
		WorkspaceID:      row.WorkspaceID,
		StartedAt:        startedAt,
		SessionKeyHash:   row.SessionKeyHash,
		SessionExpiresAt: sessionExpiresAt,
		TrustLevel:       agentauth.TrustLevel(row.TrustLevel),
		Status:           agentauth.AgentStatus(row.Status),
		LastSeenAt:       lastSeenAt,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}
