package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// DefaultRetention 是审计保留期的默认值（REQ-AUDIT-005 `[假设]`）。
const DefaultRetention = 180 * 24 * time.Hour

// MinRetention 是保留期下限（REQ-AUDIT-005 AC2）。
//
// 下限不是为了省事：账本的用处在于事后能回看，保留期短到一周以内时，
// 「上周那次放行是怎么回事」就永远问不出答案了。
const MinRetention = 7 * 24 * time.Hour

// PruneResult 是一次清理的结果。
type PruneResult struct {
	// Cutoff 是这次清理的截止时刻，早于它的记录被删除。
	Cutoff time.Time
	// PrunedCount 是实际删除的条数。
	PrunedCount int
	// Event 是随之写下的 audit.pruned 事件，与删除在同一个事务里完成。
	Event Event
}

// Pruner 按保留期清理超期的账本记录。
//
// 它无法「只删不记」：删除与 audit.pruned 事件由仓储在同一个事务里写入
// （REQ-AUDIT-005 AC1）。也无法清空账本 —— 删除始终带时间条件，
// 且保留期有下限。
type Pruner struct {
	recorder  *Recorder
	retention time.Duration
}

// NewPruner 组装清理器。retention 小于 MinRetention 时拒绝构造：
// 一个过短的保留期造成的损失是不可逆的，不该等到执行时才发现。
func NewPruner(recorder *Recorder, retention time.Duration) (*Pruner, error) {
	if recorder == nil {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("清理器需要一个审计写入器")
	}
	if retention < MinRetention {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("审计保留期不得短于 " + MinRetention.String() + "，收到 " + retention.String())
	}
	return &Pruner{recorder: recorder, retention: retention}, nil
}

func (p *Pruner) Retention() time.Duration { return p.retention }

// Prune 删除超过保留期的记录，并写下一条 audit.pruned 事件。
//
// 删除条数在删除前先数一遍，事件里如实写下这个数字；两件事在同一个事务里，
// 因此不存在「记下的数字与实际删掉的不一致」。
func (p *Pruner) Prune(ctx context.Context) (PruneResult, error) {
	now := p.recorder.clock.Now()
	cutoff := now.Add(-p.retention)

	counted, err := p.recorder.repository.CountBefore(ctx, cutoff)
	if err != nil {
		return PruneResult{}, err
	}

	record, err := p.pruneEvent(counted, cutoff)
	if err != nil {
		return PruneResult{}, err
	}

	pruned, written, err := p.recorder.repository.PruneBefore(ctx, cutoff, record)
	if err != nil {
		return PruneResult{}, err
	}
	if pruned != counted {
		// 同一个事务里数与删，对不上说明有别的写入者绕过了这条路径。
		return PruneResult{}, apperr.New(apperr.CodeInternal).
			WithDetail("清理条数与统计不一致，账本可能被本路径之外的写入改动过")
	}

	return PruneResult{Cutoff: cutoff, PrunedCount: pruned, Event: written}, nil
}

func (p *Pruner) pruneEvent(counted int, cutoff time.Time) (Event, error) {
	metadata, err := json.Marshal(map[string]any{
		"pruned_count":   counted,
		"cutoff":         cutoff.UTC().Format(time.RFC3339Nano),
		"retention_days": int(p.retention.Hours() / 24),
	})
	if err != nil {
		return Event{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("构造清理事件的元数据失败")
	}

	// 清理也是一次操作，同样要有能在账本里定位它的追溯号。
	operationID, err := p.recorder.ids.NewID()
	if err != nil {
		return Event{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("生成清理操作的 operation_id 失败")
	}

	return p.recorder.prepare(Event{
		OperationID:   operationID,
		Type:          EventPruned,
		Service:       "opendelo",
		Operation:     "audit.prune",
		Resource:      `{"table":"audit_events"}`,
		ResolvedScope: `{"scope":"audit_retention"}`,
		Outcome:       OutcomeSucceeded,
		Metadata:      string(metadata),
	})
}
