package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// CapabilityRequests 是 pipeline.CapabilityRequestRepository 的 SQLite 实现。
type CapabilityRequests struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ pipeline.CapabilityRequestRepository = (*CapabilityRequests)(nil)

// NewCapabilityRequests 绑定到已迁移的数据库。
func NewCapabilityRequests(db *store.DB) *CapabilityRequests {
	return &CapabilityRequests{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (c *CapabilityRequests) CreateRequest(
	ctx context.Context, request pipeline.CapabilityRequest,
) (pipeline.CapabilityRequest, error) {
	row, err := c.write.CreateCapabilityRequest(ctx, queries.CreateCapabilityRequestParams{
		ID:            request.ID,
		OperationID:   request.OperationID,
		AgentID:       request.AgentID,
		WorkspaceID:   request.WorkspaceID,
		Service:       request.Service,
		Operation:     request.Operation,
		Resource:      request.Resource,
		DesiredChange: optionalText(request.DesiredChange),
		Reason:        request.Reason,
		Status:        string(request.Status),
		CreatedAt:     encodeTime(request.CreatedAt),
		UpdatedAt:     encodeTime(request.UpdatedAt),
	})
	if err != nil {
		return pipeline.CapabilityRequest{}, writeError(err, "写入能力请求 "+request.ID+" 失败")
	}
	return toRequest(row)
}

func (c *CapabilityRequests) RequestByID(
	ctx context.Context, id string,
) (pipeline.CapabilityRequest, error) {
	row, err := c.read.GetCapabilityRequestByID(ctx, id)
	if err != nil {
		return pipeline.CapabilityRequest{}, readError(err, "读取能力请求 "+id+" 失败")
	}
	return toRequest(row)
}

func (c *CapabilityRequests) RequestsByStatus(
	ctx context.Context, status pipeline.RequestStatus, limit int,
) ([]pipeline.CapabilityRequest, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := c.read.ListCapabilityRequestsByStatus(ctx,
		queries.ListCapabilityRequestsByStatusParams{Status: string(status), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出状态为 "+string(status)+" 的能力请求失败")
	}

	requests := make([]pipeline.CapabilityRequest, 0, len(rows))
	for _, row := range rows {
		request, convertErr := toRequest(row)
		if convertErr != nil {
			return nil, convertErr
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// AdvanceRequest 用条件更新推进状态。WHERE 里带上 from 之后，并发的两次推进
// 只有一个会影响到行，另一个拿到零行并被翻译成 conflict。
// 读回来再判断则挡不住这种竞态。
func (c *CapabilityRequests) AdvanceRequest(
	ctx context.Context, id string, from, to pipeline.RequestStatus, at time.Time,
) (pipeline.CapabilityRequest, error) {
	row, err := c.write.UpdateCapabilityRequestStatus(ctx,
		queries.UpdateCapabilityRequestStatusParams{
			Status:    string(to),
			UpdatedAt: encodeTime(at),
			ID:        id,
			Status_2:  string(from),
		})
	if errors.Is(err, sql.ErrNoRows) {
		return pipeline.CapabilityRequest{}, apperr.Wrap(apperr.CodeConflict, err).
			WithDetail("能力请求 " + id + " 当前不处于 " + string(from) + "，无法推进到 " + string(to))
	}
	if err != nil {
		return pipeline.CapabilityRequest{}, writeError(err, "推进能力请求 "+id+" 的状态失败")
	}
	return toRequest(row)
}

// SaveChangePreview 写入查勘结果，只在请求仍处于 expected 时生效。
//
// 与 AdvanceRequest 同一种写法：条件在 WHERE 里，零行即冲突。先读再判会在
// 「读到还在等人」与「写下旧值」之间留一段窗口，而人恰好在那一段里按下允许。
func (c *CapabilityRequests) SaveChangePreview(
	ctx context.Context, id, preview string, expected pipeline.RequestStatus, at time.Time,
) error {
	affected, err := c.write.UpdateCapabilityRequestChangePreview(ctx,
		queries.UpdateCapabilityRequestChangePreviewParams{
			ChangePreview: optionalText(preview),
			UpdatedAt:     encodeTime(at),
			ID:            id,
			Status:        string(expected),
		})
	if err != nil {
		return writeError(err, "写入能力请求 "+id+" 的查勘结果失败")
	}
	if affected == 0 {
		return apperr.New(apperr.CodeConflict).
			WithDetail("能力请求 " + id + " 已不处于 " + string(expected) + "，查勘结果不再写入")
	}
	return nil
}

func toRequest(row queries.CapabilityRequest) (pipeline.CapabilityRequest, error) {
	createdAt, err := decodeTime("capability_requests.created_at", row.CreatedAt)
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}
	updatedAt, err := decodeTime("capability_requests.updated_at", row.UpdatedAt)
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}

	return pipeline.CapabilityRequest{
		ID:            row.ID,
		OperationID:   row.OperationID,
		AgentID:       row.AgentID,
		WorkspaceID:   row.WorkspaceID,
		Service:       row.Service,
		Operation:     row.Operation,
		Resource:      row.Resource,
		DesiredChange: row.DesiredChange.String,
		Reason:        row.Reason,
		Status:        pipeline.RequestStatus(row.Status),
		ChangePreview: row.ChangePreview.String,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}, nil
}
