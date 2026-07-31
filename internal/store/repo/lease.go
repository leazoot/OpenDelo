package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Leases 是 lease.Repository 的 SQLite 实现。
//
// 没有修改 Scope 与 Capabilities 的方法（REQ-LEASE-004 AC1）：
// 这一层不提供入口，上层就无从调用。
type Leases struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ lease.Repository = (*Leases)(nil)

// NewLeases 绑定到已迁移的数据库。
func NewLeases(db *store.DB) *Leases {
	return &Leases{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (l *Leases) IssueLease(ctx context.Context, issued lease.Lease) (lease.Lease, error) {
	row, err := l.write.CreateLease(ctx, queries.CreateLeaseParams{
		ID:             issued.ID,
		AgentID:        issued.AgentID,
		IdentityID:     issued.IdentityID,
		Service:        issued.Service,
		ResourceScope:  issued.ResourceScope,
		Capabilities:   issued.Capabilities,
		ExpiresAt:      encodeTime(issued.ExpiresAt),
		RequestLimit:   optionalCount(issued.RequestLimit),
		UsedRequests:   int64(issued.UsedRequests),
		Status:         string(issued.Status),
		ApprovalID:     optionalText(issued.ApprovalID),
		IsSessionBound: encodeFlag(issued.IsSessionBound),
		SourceMemoryID: optionalText(issued.SourceMemoryID),
		CreatedAt:      encodeTime(issued.CreatedAt),
		UpdatedAt:      encodeTime(issued.UpdatedAt),
	})
	if err != nil {
		return lease.Lease{}, writeError(err, "签发 Lease "+issued.ID+" 失败")
	}
	return toLease(row)
}

func (l *Leases) LeaseByID(ctx context.Context, id string) (lease.Lease, error) {
	row, err := l.read.GetLeaseByID(ctx, id)
	if err != nil {
		return lease.Lease{}, readError(err, "读取 Lease "+id+" 失败")
	}
	return toLease(row)
}

// LeaseByApprovalID 反查某个审批项签发出来的那一条 Lease。
//
// approval_id 可为空（自动放行签发的 Lease 没有审批项），而 SQLite 的唯一索引
// 允许多个 NULL —— 空串在这里必须当成「没有这个审批项」而不是拿去查询，
// 否则会在一堆自动放行的 Lease 里任取一条。
func (l *Leases) LeaseByApprovalID(ctx context.Context, approvalID string) (lease.Lease, error) {
	if approvalID == "" {
		return lease.Lease{}, apperr.New(apperr.CodeNotFound).
			WithDetail("没有给出审批项主键")
	}

	row, err := l.read.GetLeaseByApprovalID(ctx, optionalText(approvalID))
	if err != nil {
		return lease.Lease{}, readError(err, "读取审批项 "+approvalID+" 的 Lease 失败")
	}
	return toLease(row)
}

// ActiveLeasesByIdentity 列出某个身份名下仍生效的 Lease。
func (l *Leases) ActiveLeasesByIdentity(
	ctx context.Context, identityID string, limit int,
) ([]lease.Lease, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := l.read.ListActiveLeasesByIdentity(ctx,
		queries.ListActiveLeasesByIdentityParams{IdentityID: identityID, Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出身份 "+identityID+" 名下的 Lease 失败")
	}
	return toLeases(rows)
}

// ActiveSessionBoundLeasesByAgent 列出某个 Agent 名下仍生效且绑定会话的 Lease。
func (l *Leases) ActiveSessionBoundLeasesByAgent(
	ctx context.Context, agentID string, limit int,
) ([]lease.Lease, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := l.read.ListActiveSessionBoundLeasesByAgent(ctx,
		queries.ListActiveSessionBoundLeasesByAgentParams{AgentID: agentID, Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出 Agent "+agentID+" 名下绑定会话的 Lease 失败")
	}
	return toLeases(rows)
}

func (l *Leases) LeasesByStatus(
	ctx context.Context, status lease.Status, limit int,
) ([]lease.Lease, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := l.read.ListLeasesByStatus(ctx,
		queries.ListLeasesByStatusParams{Status: string(status), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出状态为 "+string(status)+" 的 Lease 失败")
	}
	return toLeases(rows)
}

func (l *Leases) ActiveLeasesDueBefore(
	ctx context.Context, deadline time.Time, limit int,
) ([]lease.Lease, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := l.read.ListActiveLeasesDueBefore(ctx,
		queries.ListActiveLeasesDueBeforeParams{ExpiresAt: encodeTime(deadline), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出到期的 Lease 失败")
	}
	return toLeases(rows)
}

// Consume 在一条语句里同时判定「仍生效」「未到期」「未达次数上限」并递增计数。
// 三者与递增不可分开：先读后写会让两个并发请求都读到同一个未达上限的计数，
// 于是各自加一，Lease 被多用一次。
func (l *Leases) Consume(ctx context.Context, id string, now time.Time) (lease.Lease, error) {
	row, err := l.write.ConsumeLease(ctx, queries.ConsumeLeaseParams{
		UpdatedAt: encodeTime(now),
		ID:        id,
		ExpiresAt: encodeTime(now),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return lease.Lease{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("Lease " + id + " 已不可用：不在生效中、已到期或次数已用尽")
	}
	if err != nil {
		return lease.Lease{}, writeError(err, "记录 Lease "+id+" 的一次使用失败")
	}
	return toLease(row)
}

func (l *Leases) Close(
	ctx context.Context, id string, status lease.Status, at time.Time,
) (lease.Lease, error) {
	if status == lease.StatusActive {
		return lease.Lease{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("Lease " + id + " 不能被关闭为 active")
	}

	row, err := l.write.CloseLease(ctx, queries.CloseLeaseParams{
		Status:    string(status),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return lease.Lease{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("Lease " + id + " 已不在生效中")
	}
	if err != nil {
		return lease.Lease{}, writeError(err, "关闭 Lease "+id+" 失败")
	}
	return toLease(row)
}

// Shorten 只在新的到期时刻早于原有时刻时生效：同一个值既写进 SET，
// 又作为 WHERE 的比较对象，因此这个方法不可能被用来延长授权。
func (l *Leases) Shorten(
	ctx context.Context, id string, expiresAt, at time.Time,
) (lease.Lease, error) {
	encoded := encodeTime(expiresAt)
	row, err := l.write.ShortenLease(ctx, queries.ShortenLeaseParams{
		ExpiresAt:   encoded,
		UpdatedAt:   encodeTime(at),
		ID:          id,
		ExpiresAt_2: encoded,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return lease.Lease{}, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("Lease " + id + " 不在生效中，或新的到期时间不早于原有到期时间")
	}
	if err != nil {
		return lease.Lease{}, writeError(err, "缩短 Lease "+id+" 失败")
	}
	return toLease(row)
}

func toLeases(rows []queries.Lease) ([]lease.Lease, error) {
	leases := make([]lease.Lease, 0, len(rows))
	for _, row := range rows {
		issued, err := toLease(row)
		if err != nil {
			return nil, err
		}
		leases = append(leases, issued)
	}
	return leases, nil
}

func toLease(row queries.Lease) (lease.Lease, error) {
	expiresAt, err := decodeTime("leases.expires_at", row.ExpiresAt)
	if err != nil {
		return lease.Lease{}, err
	}
	createdAt, err := decodeTime("leases.created_at", row.CreatedAt)
	if err != nil {
		return lease.Lease{}, err
	}
	updatedAt, err := decodeTime("leases.updated_at", row.UpdatedAt)
	if err != nil {
		return lease.Lease{}, err
	}
	requestLimit, err := decodeOptionalCount("leases.request_limit", row.RequestLimit)
	if err != nil {
		return lease.Lease{}, err
	}
	usedRequests, err := decodeCount("leases.used_requests", row.UsedRequests)
	if err != nil {
		return lease.Lease{}, err
	}
	isSessionBound, err := decodeFlag("leases.is_session_bound", row.IsSessionBound)
	if err != nil {
		return lease.Lease{}, err
	}

	return lease.Lease{
		ID:             row.ID,
		AgentID:        row.AgentID,
		IdentityID:     row.IdentityID,
		Service:        row.Service,
		ResourceScope:  row.ResourceScope,
		Capabilities:   row.Capabilities,
		ExpiresAt:      expiresAt,
		RequestLimit:   requestLimit,
		UsedRequests:   usedRequests,
		Status:         lease.Status(row.Status),
		ApprovalID:     row.ApprovalID.String,
		IsSessionBound: isSessionBound,
		SourceMemoryID: row.SourceMemoryID.String,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}

// RevokeLeasesByCredentialReference 撤销依赖某份凭据引用的全部活跃 Lease
// （REQ-CRED-005 AC3）。
//
// 逐条走 Close 而不是一条 UPDATE：Close 的条件更新保证只有仍生效的那些被改动，
// 而一条批量语句在并发下会把刚被别处收回的 Lease 再写一次，让审计上出现
// 两条互相矛盾的记录。撤销途中出错即返回，已撤销的保持撤销状态 ——
// 一个「凭据已断开但授权还在」的状态比「断开没全成功」危险得多。
func (l *Leases) RevokeLeasesByCredentialReference(
	ctx context.Context, referenceID string, at time.Time,
) ([]string, error) {
	if referenceID == "" {
		return nil, apperr.New(apperr.CodeInvalidRequest).WithDetail("没有给出凭据引用")
	}

	rows, err := l.read.ListActiveLeasesByCredentialReference(ctx,
		queries.ListActiveLeasesByCredentialReferenceParams{
			CredentialReferenceID: referenceID,
			Limit:                 revocationLimit,
		})
	if err != nil {
		return nil, readError(err, "列出依赖凭据引用 "+referenceID+" 的 Lease 失败")
	}

	revoked := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, err := l.Close(ctx, row.ID, lease.StatusRevoked, at); err != nil {
			return revoked, err
		}
		revoked = append(revoked, row.ID)
	}
	return revoked, nil
}

// revocationLimit 是一次断开最多撤销多少条 Lease。
//
// 无界查询在这一层就被排除。取 1000：
// 单份凭据同时有上千条活跃 Lease 已经远超本产品的使用形态，
// 真出现了也说明有别的问题要先查。
const revocationLimit = 1000
