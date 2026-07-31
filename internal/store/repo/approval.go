package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Approvals 是 approval.Repository 的 SQLite 实现。
type Approvals struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ approval.Repository = (*Approvals)(nil)

// NewApprovals 绑定到已迁移的数据库。
func NewApprovals(db *store.DB) *Approvals {
	return &Approvals{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (a *Approvals) CreateApproval(
	ctx context.Context, item approval.Approval,
) (approval.Approval, error) {
	row, err := a.write.CreateApproval(ctx, queries.CreateApprovalParams{
		ID:         item.ID,
		DecisionID: item.DecisionID,
		Action:     optionalText(string(item.Action)),
		Status:     string(item.Status),
		ExpiresAt:  encodeTime(item.ExpiresAt),
		DecidedAt:  optionalTime(item.DecidedAt),
		CreatedAt:  encodeTime(item.CreatedAt),
		UpdatedAt:  encodeTime(item.UpdatedAt),
	})
	if err != nil {
		return approval.Approval{}, writeError(err, "写入审批项 "+item.ID+" 失败")
	}
	return toApproval(row)
}

func (a *Approvals) ApprovalByID(ctx context.Context, id string) (approval.Approval, error) {
	row, err := a.read.GetApprovalByID(ctx, id)
	if err != nil {
		return approval.Approval{}, readError(err, "读取审批项 "+id+" 失败")
	}
	return toApproval(row)
}

func (a *Approvals) ApprovalByDecisionID(
	ctx context.Context, decisionID string,
) (approval.Approval, error) {
	row, err := a.read.GetApprovalByDecisionID(ctx, decisionID)
	if err != nil {
		return approval.Approval{}, readError(err, "读取决策 "+decisionID+" 的审批项失败")
	}
	return toApproval(row)
}

func (a *Approvals) ApprovalsByStatus(
	ctx context.Context, status approval.Status, limit int,
) ([]approval.Approval, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListApprovalsByStatus(ctx,
		queries.ListApprovalsByStatusParams{Status: string(status), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出状态为 "+string(status)+" 的审批项失败")
	}
	return toApprovals(rows)
}

func (a *Approvals) PendingApprovalsDueBefore(
	ctx context.Context, deadline time.Time, limit int,
) ([]approval.Approval, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListPendingApprovalsDueBefore(ctx,
		queries.ListPendingApprovalsDueBeforeParams{ExpiresAt: encodeTime(deadline), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出超时的审批项失败")
	}
	return toApprovals(rows)
}

// Settle 用条件更新写入结果。WHERE 里带上 status = 'pending' 之后，
// 并发的两次决策只有一个会影响到行 —— 同一个审批项不能被放行两次
func (a *Approvals) Settle(
	ctx context.Context, id string, action approval.Action, status approval.Status, at time.Time,
) (approval.Approval, error) {
	row, err := a.write.SettleApproval(ctx, queries.SettleApprovalParams{
		Action:    optionalText(string(action)),
		Status:    string(status),
		DecidedAt: optionalTime(at),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Approval{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("审批项 " + id + " 已经被处理过了")
	}
	if err != nil {
		return approval.Approval{}, writeError(err, "处理审批项 "+id+" 失败")
	}
	return toApproval(row)
}

func toApprovals(rows []queries.Approval) ([]approval.Approval, error) {
	items := make([]approval.Approval, 0, len(rows))
	for _, row := range rows {
		item, err := toApproval(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func toApproval(row queries.Approval) (approval.Approval, error) {
	expiresAt, err := decodeTime("approvals.expires_at", row.ExpiresAt)
	if err != nil {
		return approval.Approval{}, err
	}
	decidedAt, err := decodeOptionalTime("approvals.decided_at", row.DecidedAt)
	if err != nil {
		return approval.Approval{}, err
	}
	createdAt, err := decodeTime("approvals.created_at", row.CreatedAt)
	if err != nil {
		return approval.Approval{}, err
	}
	updatedAt, err := decodeTime("approvals.updated_at", row.UpdatedAt)
	if err != nil {
		return approval.Approval{}, err
	}

	return approval.Approval{
		ID:         row.ID,
		DecisionID: row.DecisionID,
		Action:     approval.Action(row.Action.String),
		Status:     approval.Status(row.Status),
		ExpiresAt:  expiresAt,
		DecidedAt:  decidedAt,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}, nil
}
