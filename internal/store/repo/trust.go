package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// TrustMemories 是 trust.Repository 的 SQLite 实现。
//
// 没有修改 Scope 的方法：改范围就是重新学习，只能由一次新的审批产生新记忆。
type TrustMemories struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ trust.Repository = (*TrustMemories)(nil)

// NewTrustMemories 绑定到已迁移的数据库。
func NewTrustMemories(db *store.DB) *TrustMemories {
	return &TrustMemories{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (m *TrustMemories) CreateMemory(ctx context.Context, memory trust.Memory) (trust.Memory, error) {
	row, err := m.write.CreateTrustMemory(ctx, queries.CreateTrustMemoryParams{
		ID:                 memory.ID,
		AgentID:            memory.AgentID,
		WorkspaceID:        memory.WorkspaceID,
		IdentityID:         memory.IdentityID,
		Service:            memory.Service,
		ResourceScope:      memory.ResourceScope,
		CapabilityScope:    memory.CapabilityScope,
		Environment:        string(memory.Environment),
		RiskCeiling:        string(memory.RiskCeiling),
		ApprovalBehavior:   string(memory.Behavior),
		CreatedFrom:        memory.CreatedFrom,
		Status:             string(memory.Status),
		InvalidationReason: optionalText(string(memory.InvalidationReason)),
		LastUsedAt:         optionalTime(memory.LastUsedAt),
		ExpiresAt:          encodeTime(memory.ExpiresAt),
		CreatedAt:          encodeTime(memory.CreatedAt),
		UpdatedAt:          encodeTime(memory.UpdatedAt),
	})
	if err != nil {
		return trust.Memory{}, writeError(err, "写入授权记忆 "+memory.ID+" 失败")
	}
	return toMemory(row)
}

func (m *TrustMemories) MemoryByID(ctx context.Context, id string) (trust.Memory, error) {
	row, err := m.read.GetTrustMemoryByID(ctx, id)
	if err != nil {
		return trust.Memory{}, readError(err, "读取授权记忆 "+id+" 失败")
	}
	return toMemory(row)
}

// MatchMemories 只返回生效中的记忆：失效的记忆读得到，但匹配不到，
// 因此下一次同类请求会重新进入审批（REQ-TRUST-004 AC3）。
func (m *TrustMemories) MatchMemories(
	ctx context.Context, agentID, workspaceID, service string, limit int,
) ([]trust.Memory, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := m.read.MatchTrustMemories(ctx, queries.MatchTrustMemoriesParams{
		AgentID:     agentID,
		WorkspaceID: workspaceID,
		Service:     service,
		Limit:       int64(limit),
	})
	if err != nil {
		return nil, readError(err, "匹配服务 "+service+" 的授权记忆失败")
	}
	return toMemories(rows)
}

func (m *TrustMemories) MemoriesByStatus(
	ctx context.Context, status trust.Status, limit int,
) ([]trust.Memory, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := m.read.ListTrustMemoriesByStatus(ctx,
		queries.ListTrustMemoriesByStatusParams{Status: string(status), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出状态为 "+string(status)+" 的授权记忆失败")
	}
	return toMemories(rows)
}

// ActiveMemoriesByIdentity 列出某个身份名下仍生效的记忆。
func (m *TrustMemories) ActiveMemoriesByIdentity(
	ctx context.Context, identityID string, limit int,
) ([]trust.Memory, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := m.read.ListTrustMemoriesByIdentity(ctx,
		queries.ListTrustMemoriesByIdentityParams{IdentityID: identityID, Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出身份 "+identityID+" 名下的授权记忆失败")
	}
	return toMemories(rows)
}

func (m *TrustMemories) Touch(ctx context.Context, id string, at time.Time) (trust.Memory, error) {
	row, err := m.write.TouchTrustMemory(ctx, queries.TouchTrustMemoryParams{
		LastUsedAt: optionalTime(at),
		UpdatedAt:  encodeTime(at),
		ID:         id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return trust.Memory{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("授权记忆 " + id + " 已失效，不能记为使用过")
	}
	if err != nil {
		return trust.Memory{}, writeError(err, "刷新授权记忆 "+id+" 的使用时刻失败")
	}
	return toMemory(row)
}

// TightenBehavior 把一条记忆从「自动允许」改成「始终询问」。
//
// 只有这一个方向：反方向在 SQL 里就不可表达。已失效的记忆改不动 ——
// 那不是收紧，是在复活一条已经被判定不该再生效的授权。
func (m *TrustMemories) TightenBehavior(
	ctx context.Context, id string, at time.Time,
) (trust.Memory, error) {
	row, err := m.write.TightenTrustMemoryBehavior(ctx,
		queries.TightenTrustMemoryBehaviorParams{UpdatedAt: encodeTime(at), ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return trust.Memory{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("授权记忆 " + id + " 不在生效中，或本来就是「始终询问」")
	}
	if err != nil {
		return trust.Memory{}, writeError(err, "收紧授权记忆 "+id+" 的行为失败")
	}
	return toMemory(row)
}

func (m *TrustMemories) Invalidate(
	ctx context.Context, id string, reason trust.InvalidationReason, at time.Time,
) (trust.Memory, error) {
	if reason == "" {
		return trust.Memory{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("使授权记忆 " + id + " 失效必须给出原因")
	}

	row, err := m.write.InvalidateTrustMemory(ctx, queries.InvalidateTrustMemoryParams{
		InvalidationReason: optionalText(string(reason)),
		UpdatedAt:          encodeTime(at),
		ID:                 id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return trust.Memory{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("授权记忆 " + id + " 已经失效了")
	}
	if err != nil {
		return trust.Memory{}, writeError(err, "使授权记忆 "+id+" 失效失败")
	}
	return toMemory(row)
}

// DeleteMemory 删除用户不想再要的记忆（REQ-TRUST-005 AC1）。
// 删不到行时报 not_found 而不是当作成功：调用方以为删掉了、实际没删，
// 那条记忆下一次还会自动放行。
func (m *TrustMemories) DeleteMemory(ctx context.Context, id string) error {
	affected, err := m.write.DeleteTrustMemory(ctx, id)
	if err != nil {
		return writeError(err, "删除授权记忆 "+id+" 失败")
	}
	if affected == 0 {
		return apperr.New(apperr.CodeNotFound).WithDetail("授权记忆 " + id + " 不存在")
	}
	return nil
}

func toMemories(rows []queries.TrustMemory) ([]trust.Memory, error) {
	memories := make([]trust.Memory, 0, len(rows))
	for _, row := range rows {
		memory, err := toMemory(row)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, nil
}

func toMemory(row queries.TrustMemory) (trust.Memory, error) {
	lastUsedAt, err := decodeOptionalTime("trust_memories.last_used_at", row.LastUsedAt)
	if err != nil {
		return trust.Memory{}, err
	}
	expiresAt, err := decodeTime("trust_memories.expires_at", row.ExpiresAt)
	if err != nil {
		return trust.Memory{}, err
	}
	createdAt, err := decodeTime("trust_memories.created_at", row.CreatedAt)
	if err != nil {
		return trust.Memory{}, err
	}
	updatedAt, err := decodeTime("trust_memories.updated_at", row.UpdatedAt)
	if err != nil {
		return trust.Memory{}, err
	}

	return trust.Memory{
		ID:                 row.ID,
		AgentID:            row.AgentID,
		WorkspaceID:        row.WorkspaceID,
		IdentityID:         row.IdentityID,
		Service:            row.Service,
		ResourceScope:      row.ResourceScope,
		CapabilityScope:    row.CapabilityScope,
		Environment:        matcher.Environment(row.Environment),
		RiskCeiling:        risk.Level(row.RiskCeiling),
		Behavior:           trust.Behavior(row.ApprovalBehavior),
		CreatedFrom:        row.CreatedFrom,
		Status:             trust.Status(row.Status),
		InvalidationReason: trust.InvalidationReason(row.InvalidationReason.String),
		LastUsedAt:         lastUsedAt,
		ExpiresAt:          expiresAt,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
	}, nil
}
