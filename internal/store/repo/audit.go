package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// AuditEvents 是 audit.Repository 的 SQLite 实现。
//
// 只追加：这里没有任何 UPDATE 语句。唯一的删除入口按时间条件执行，
// 且拒绝零值时刻 —— 一次误调用不该能清空账本。
type AuditEvents struct {
	read   *queries.Queries
	write  *queries.Queries
	writer *sql.DB
}

var _ audit.Repository = (*AuditEvents)(nil)

// NewAuditEvents 绑定到已迁移的数据库。
func NewAuditEvents(db *store.DB) *AuditEvents {
	return &AuditEvents{
		read:   queries.New(db.Reader()),
		write:  queries.New(db.Writer()),
		writer: db.Writer(),
	}
}

func (a *AuditEvents) Append(ctx context.Context, event audit.Event) (audit.Event, error) {
	row, err := a.write.AppendAuditEvent(ctx, appendParams(event))
	if err != nil {
		return audit.Event{}, writeError(err, "写入审计事件 "+event.ID+" 失败")
	}
	return toEvent(row)
}

// appendParams 把领域事件转成写入参数。Append 与 PruneBefore 共用它，
// 两条路径因此不可能对同一个字段有不同处理。
func appendParams(event audit.Event) queries.AppendAuditEventParams {
	return queries.AppendAuditEventParams{
		ID:                   event.ID,
		OperationID:          event.OperationID,
		EventType:            string(event.Type),
		AgentID:              optionalText(event.AgentID),
		DeviceID:             optionalText(event.DeviceID),
		WorkspaceID:          optionalText(event.WorkspaceID),
		IdentityID:           optionalText(event.IdentityID),
		CredentialProviderID: optionalText(event.CredentialProviderID),
		Service:              event.Service,
		Operation:            event.Operation,
		Resource:             event.Resource,
		ResolvedScope:        event.ResolvedScope,
		Verdict:              optionalText(string(event.Verdict)),
		RiskLevel:            optionalText(string(event.RiskLevel)),
		DecisionID:           optionalText(event.DecisionID),
		ApprovalID:           optionalText(event.ApprovalID),
		LeaseID:              optionalText(event.LeaseID),
		LeaseStatus:          optionalText(string(event.LeaseStatus)),
		Outcome:              string(event.Outcome),
		ResponseStatus:       optionalCount(event.ResponseStatus),
		DurationMs:           event.Duration.Milliseconds(),
		IsRedacted:           encodeFlag(event.IsRedacted),
		Metadata:             event.Metadata,
		CreatedAt:            encodeTime(event.CreatedAt),
	}
}

func (a *AuditEvents) EventByID(ctx context.Context, id string) (audit.Event, error) {
	row, err := a.read.GetAuditEventByID(ctx, id)
	if err != nil {
		return audit.Event{}, readError(err, "读取审计事件 "+id+" 失败")
	}
	return toEvent(row)
}

func (a *AuditEvents) Events(
	ctx context.Context, before time.Time, limit int,
) ([]audit.Event, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListAuditEvents(ctx,
		queries.ListAuditEventsParams{CreatedAt: encodeCursor(before), Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出审计事件失败")
	}
	return toEvents(rows)
}

func (a *AuditEvents) EventsByAgent(
	ctx context.Context, agentID string, before time.Time, limit int,
) ([]audit.Event, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListAuditEventsByAgent(ctx, queries.ListAuditEventsByAgentParams{
		AgentID:   optionalText(agentID),
		CreatedAt: encodeCursor(before),
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, readError(err, "列出 Agent "+agentID+" 的审计事件失败")
	}
	return toEvents(rows)
}

func (a *AuditEvents) EventsByService(
	ctx context.Context, service string, before time.Time, limit int,
) ([]audit.Event, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := a.read.ListAuditEventsByService(ctx, queries.ListAuditEventsByServiceParams{
		Service:   service,
		CreatedAt: encodeCursor(before),
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, readError(err, "列出服务 "+service+" 的审计事件失败")
	}
	return toEvents(rows)
}

func (a *AuditEvents) CountBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if cutoff.IsZero() {
		return 0, zeroCutoff()
	}

	counted, err := a.read.CountAuditEventsBefore(ctx, encodeTime(cutoff))
	if err != nil {
		return 0, readError(err, "统计超期审计事件失败")
	}
	return decodeCount("count(*)", counted)
}

// PruneBefore 是账本上唯一的删除入口，删除与记录在同一个事务里完成。
//
// 零值 cutoff 会被拒绝：它编码出来是一个远早于任何记录的时刻，
// 看起来「什么都不会删」，但把判断交给巧合不是安全边界该有的样子。
func (a *AuditEvents) PruneBefore(
	ctx context.Context, cutoff time.Time, record audit.Event,
) (count int, written audit.Event, err error) {
	if cutoff.IsZero() {
		return 0, audit.Event{}, zeroCutoff()
	}
	if record.CreatedAt.Before(cutoff) {
		// 清理事件自己就在保留期外，写进去下一轮就被删掉，账本上等于没记过。
		return 0, audit.Event{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("清理事件的发生时刻早于保留期截止时刻")
	}

	transaction, err := a.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, audit.Event{}, writeError(err, "开启清理事务失败")
	}
	// 提交之后回滚会返回 ErrTxDone，那是正常路径；其余错误说明事务没能干净收场，
	// 必须并进返回值 —— 悬着的事务会一直占着 SQLite 唯一的写连接。
	defer func() {
		rollbackErr := transaction.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, writeError(rollbackErr, "回滚清理事务失败"))
		}
	}()

	inTransaction := a.write.WithTx(transaction)

	pruned, err := inTransaction.PruneAuditEventsBefore(ctx, encodeTime(cutoff))
	if err != nil {
		return 0, audit.Event{}, writeError(err, "清理超期审计事件失败")
	}
	count, err = decodeCount("pruned", pruned)
	if err != nil {
		return 0, audit.Event{}, err
	}

	row, err := inTransaction.AppendAuditEvent(ctx, appendParams(record))
	if err != nil {
		return 0, audit.Event{}, writeError(err, "写入清理事件 "+record.ID+" 失败")
	}
	written, err = toEvent(row)
	if err != nil {
		return 0, audit.Event{}, err
	}

	if err := transaction.Commit(); err != nil {
		return 0, audit.Event{}, writeError(err, "提交清理事务失败")
	}
	return count, written, nil
}

func zeroCutoff() error {
	return apperr.New(apperr.CodeInvalidRequest).WithDetail("审计保留期的截止时刻不能为空")
}

// encodeCursor 把翻页游标转成比较用的文本。零值表示「从最新一条开始」，
// 编码成一个不可能被任何记录超过的时刻。
func encodeCursor(before time.Time) string {
	if before.IsZero() {
		return "9999-12-31T23:59:59.999Z"
	}
	return encodeTime(before)
}

func toEvents(rows []queries.AuditEvent) ([]audit.Event, error) {
	events := make([]audit.Event, 0, len(rows))
	for _, row := range rows {
		event, err := toEvent(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func toEvent(row queries.AuditEvent) (audit.Event, error) {
	createdAt, err := decodeTime("audit_events.created_at", row.CreatedAt)
	if err != nil {
		return audit.Event{}, err
	}
	responseStatus, err := decodeOptionalCount("audit_events.response_status", row.ResponseStatus)
	if err != nil {
		return audit.Event{}, err
	}
	duration, err := decodeCount("audit_events.duration_ms", row.DurationMs)
	if err != nil {
		return audit.Event{}, err
	}
	isRedacted, err := decodeFlag("audit_events.is_redacted", row.IsRedacted)
	if err != nil {
		return audit.Event{}, err
	}

	return audit.Event{
		ID:                   row.ID,
		OperationID:          row.OperationID,
		Type:                 audit.EventType(row.EventType),
		AgentID:              row.AgentID.String,
		DeviceID:             row.DeviceID.String,
		WorkspaceID:          row.WorkspaceID.String,
		IdentityID:           row.IdentityID.String,
		CredentialProviderID: row.CredentialProviderID.String,
		Service:              row.Service,
		Operation:            row.Operation,
		Resource:             row.Resource,
		ResolvedScope:        row.ResolvedScope,
		Verdict:              audit.Verdict(row.Verdict.String),
		RiskLevel:            audit.RiskLevel(row.RiskLevel.String),
		DecisionID:           row.DecisionID.String,
		ApprovalID:           row.ApprovalID.String,
		LeaseID:              row.LeaseID.String,
		LeaseStatus:          audit.LeaseStatus(row.LeaseStatus.String),
		Outcome:              audit.Outcome(row.Outcome),
		ResponseStatus:       responseStatus,
		Duration:             time.Duration(duration) * time.Millisecond,
		IsRedacted:           isRedacted,
		Metadata:             row.Metadata,
		CreatedAt:            createdAt,
	}, nil
}
