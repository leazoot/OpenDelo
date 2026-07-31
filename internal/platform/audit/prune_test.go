package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// prunableRepository 记下清理调用的参数，并可按需失败。
type prunableRepository struct {
	audit.Repository

	counted     int
	countErr    error
	pruneErr    error
	prunedCount int

	seenCutoff time.Time
	seenRecord audit.Event
}

func (p *prunableRepository) CountBefore(_ context.Context, cutoff time.Time) (int, error) {
	if p.countErr != nil {
		return 0, p.countErr
	}
	p.seenCutoff = cutoff
	return p.counted, nil
}

func (p *prunableRepository) PruneBefore(
	_ context.Context, cutoff time.Time, record audit.Event,
) (int, audit.Event, error) {
	if p.pruneErr != nil {
		return 0, audit.Event{}, p.pruneErr
	}
	p.seenCutoff = cutoff
	p.seenRecord = record
	if p.prunedCount == 0 {
		p.prunedCount = p.counted
	}
	return p.prunedCount, record, nil
}

func newPruner(t *testing.T, repository audit.Repository, retention time.Duration) *audit.Pruner {
	t.Helper()

	recorder, err := audit.NewRecorder(repository, clock.NewFixed(recordedAt), ulid.New(clock.NewFixed(recordedAt)))
	if err != nil {
		t.Fatalf("组装写入器失败：%v", err)
	}
	pruner, err := audit.NewPruner(recorder, retention)
	if err != nil {
		t.Fatalf("组装清理器失败：%v", err)
	}
	return pruner
}

func TestNewPruner_RetentionBelowTheFloor_IsRejected(t *testing.T) {
	// REQ-AUDIT-005 AC2：最小 7 天。过短的保留期造成的损失不可逆，
	// 不该等到执行时才发现。
	recorder, err := audit.NewRecorder(&fakeRepository{},
		clock.NewFixed(recordedAt), ulid.New(clock.NewFixed(recordedAt)))
	if err != nil {
		t.Fatalf("组装写入器失败：%v", err)
	}

	for _, retention := range []time.Duration{0, time.Hour, audit.MinRetention - time.Second} {
		_, err := audit.NewPruner(recorder, retention)
		assertCode(t, err, apperr.CodeInvalidConfiguration)
	}

	if _, err := audit.NewPruner(recorder, audit.MinRetention); err != nil {
		t.Errorf("恰好等于下限的保留期被拒绝：%v", err)
	}
	if _, err := audit.NewPruner(nil, audit.DefaultRetention); err == nil {
		t.Error("没有写入器的清理器被造出来了")
	}
}

func TestPruner_DefaultRetention_IsOneHundredEightyDays(t *testing.T) {
	pruner := newPruner(t, &prunableRepository{}, audit.DefaultRetention)

	if pruner.Retention() != 180*24*time.Hour {
		t.Errorf("默认保留期是 %s，期望 180 天", pruner.Retention())
	}
}

func TestPruner_CutoffComesFromTheClockMinusRetention(t *testing.T) {
	// 截止时刻由网关时钟推出来，不接受外部给定 —— 给定一个未来时刻就能清空账本。
	ctx := t.Context()
	repository := &prunableRepository{counted: 3}
	pruner := newPruner(t, repository, audit.DefaultRetention)

	result, err := pruner.Prune(ctx)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}

	want := recordedAt.Add(-audit.DefaultRetention)
	if !result.Cutoff.Equal(want) {
		t.Errorf("截止时刻是 %v，期望 %v", result.Cutoff, want)
	}
	if !repository.seenCutoff.Equal(want) {
		t.Errorf("交给仓储的截止时刻是 %v，期望 %v", repository.seenCutoff, want)
	}
	if result.PrunedCount != 3 {
		t.Errorf("清理了 %d 条，期望 3 条", result.PrunedCount)
	}
}

func TestPruner_RecordsAPrunedEventWithTheRealCount(t *testing.T) {
	// REQ-AUDIT-005 AC1：清理必须产生 audit.pruned 事件，且数字如实。
	ctx := t.Context()
	repository := &prunableRepository{counted: 42}
	pruner := newPruner(t, repository, audit.DefaultRetention)

	result, err := pruner.Prune(ctx)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}

	if result.Event.Type != audit.EventPruned {
		t.Errorf("事件类型是 %q，期望 audit.pruned", result.Event.Type)
	}
	if result.Event.OperationID == "" {
		t.Error("清理事件没有 operation_id，这条记录无从定位")
	}
	if !result.Event.CreatedAt.Equal(recordedAt) {
		t.Errorf("清理事件的时刻是 %v，期望取自网关时钟", result.Event.CreatedAt)
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(result.Event.Metadata), &metadata); err != nil {
		t.Fatalf("解析清理事件的元数据失败：%v", err)
	}
	if metadata["pruned_count"] != float64(42) {
		t.Errorf("元数据里的条数是 %v，期望 42", metadata["pruned_count"])
	}
	if metadata["retention_days"] != float64(180) {
		t.Errorf("元数据里的保留天数是 %v，期望 180", metadata["retention_days"])
	}

	// 事件本身的时刻必须晚于截止时刻，否则下一轮清理会把它一起删掉。
	if !result.Event.CreatedAt.After(result.Cutoff) {
		t.Error("清理事件落在了自己删掉的时间窗里")
	}
}

func TestPruner_NothingToPrune_StillRecordsTheRun(t *testing.T) {
	// 一次「什么都没删」的清理同样要留痕：否则看账本的人分不清
	// 「没执行过」与「执行了但没有超期记录」。
	ctx := t.Context()
	repository := &prunableRepository{counted: 0}
	pruner := newPruner(t, repository, audit.DefaultRetention)

	result, err := pruner.Prune(ctx)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if result.PrunedCount != 0 {
		t.Errorf("清理了 %d 条，期望 0 条", result.PrunedCount)
	}
	if repository.seenRecord.Type != audit.EventPruned {
		t.Error("没有超期记录时，清理事件没有被写下")
	}
}

func TestPruner_CountAndDeleteMismatch_IsReported(t *testing.T) {
	// 同一个事务里数与删，对不上说明有别的写入者绕过了这条路径。
	// 这时报错而不是把差额咽下去。
	ctx := t.Context()
	repository := &prunableRepository{counted: 5, prunedCount: 7}
	pruner := newPruner(t, repository, audit.DefaultRetention)

	_, err := pruner.Prune(ctx)
	assertCode(t, err, apperr.CodeInternal)
}

func TestPruner_RepositoryFailure_Propagates(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name       string
		repository *prunableRepository
	}{
		{name: "统计失败", repository: &prunableRepository{countErr: apperr.New(apperr.CodeInternal)}},
		{name: "清理失败", repository: &prunableRepository{pruneErr: apperr.New(apperr.CodeConflict)}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pruner := newPruner(t, testCase.repository, audit.DefaultRetention)
			if _, err := pruner.Prune(ctx); err == nil {
				t.Error("仓储失败时清理却成功了")
			}
		})
	}
}
