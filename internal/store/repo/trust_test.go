package repo_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// memoryChain 在审批链之外加上记忆仓储。
type memoryChain struct {
	chain    leaseChain
	memories *repo.TrustMemories
}

func newMemoryChain(t *testing.T) memoryChain {
	t.Helper()

	all := seededLeaseChain(t)
	return memoryChain{chain: all, memories: repo.NewTrustMemories(all.chain.db)}
}

func TestTrustMemories_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := newMemoryChain(t)
	want := fixtures.Memory()

	created, err := all.memories.CreateMemory(ctx, want)
	if err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := all.memories.MemoryByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestTrustMemories_NeverUsed_RoundTripsAsZeroTime(t *testing.T) {
	// 零值时间落成 NULL。存成一个真实时刻会让「长期未使用」的判断立刻命中。
	ctx := t.Context()
	all := newMemoryChain(t)

	created, err := all.memories.CreateMemory(ctx, fixtures.Memory())
	if err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}
	if !created.LastUsedAt.IsZero() {
		t.Errorf("从未使用过的记忆带着使用时刻 %v", created.LastUsedAt)
	}

	var stored sql.NullString
	if err := all.chain.chain.db.Reader().QueryRowContext(ctx,
		`SELECT last_used_at FROM trust_memories WHERE id = ?`,
		fixtures.DefaultMemoryID).Scan(&stored); err != nil {
		t.Fatalf("直接读取使用时刻失败：%v", err)
	}
	if stored.Valid {
		t.Errorf("库里存的是 %q，期望 NULL", stored.String)
	}
}

func TestTrustMemories_HighRiskCeiling_ReportsInvalidRequest(t *testing.T) {
	// REQ-TRUST-003：不存在能自动放行高风险的记忆。
	all := newMemoryChain(t)

	_, err := all.memories.CreateMemory(t.Context(),
		fixtures.Memory(fixtures.WithMemoryRiskCeiling(risk.LevelHigh)))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestTrustMemories_SecondMemoryFromTheSameApproval_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	all := newMemoryChain(t)

	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}
	_, err := all.memories.CreateMemory(ctx,
		fixtures.Memory(fixtures.WithMemoryID("01K1MEMORY0000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestTrustMemories_Match_OnlyReturnsActiveOnes(t *testing.T) {
	// REQ-TRUST-004 AC3：失效的记忆读得到，但匹配不到，
	// 因此下一次同类请求会重新进入审批。
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	matched, err := all.memories.MatchMemories(ctx,
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配授权记忆失败：%v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("匹配到 %d 条，期望 1 条", len(matched))
	}

	if _, invalidateErr := all.memories.Invalidate(ctx, fixtures.DefaultMemoryID,
		trust.ReasonDeviceUntrusted, fixtures.Instant); invalidateErr != nil {
		t.Fatalf("使记忆失效失败：%v", invalidateErr)
	}

	afterInvalidation, err := all.memories.MatchMemories(ctx,
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配授权记忆失败：%v", err)
	}
	if len(afterInvalidation) != 0 {
		t.Errorf("失效的记忆仍被匹配到：%d 条", len(afterInvalidation))
	}

	// 失效原因是数据的一部分，Automation 页面要显示它而不是让记忆消失。
	stored, err := all.memories.MemoryByID(ctx, fixtures.DefaultMemoryID)
	if err != nil {
		t.Fatalf("读取失效的记忆失败：%v", err)
	}
	if stored.Status != trust.StatusInvalidated {
		t.Errorf("状态是 %q，期望 invalidated", stored.Status)
	}
	if stored.InvalidationReason != trust.ReasonDeviceUntrusted {
		t.Errorf("失效原因是 %q，期望 device_untrusted", stored.InvalidationReason)
	}
}

func TestTrustMemories_Match_DoesNotCrossAgentOrWorkspaceOrService(t *testing.T) {
	// 记忆不能溢出到别的 Agent、别的项目或别的服务 —— 那正是 REQ-TRUST-002
	// 要挡住的三个维度。
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	cases := []struct {
		name        string
		agentID     string
		workspaceID string
		service     string
	}{
		{
			name:    "另一个 Agent",
			agentID: "01K1OTHERAGENT000000000000", workspaceID: fixtures.DefaultWorkspaceID,
			service: fixtures.DefaultServiceLabel,
		},
		{
			name:    "另一个项目",
			agentID: fixtures.DefaultAgentID, workspaceID: "01K1OTHERWORKSPACE00000000",
			service: fixtures.DefaultServiceLabel,
		},
		{
			name:    "另一个服务",
			agentID: fixtures.DefaultAgentID, workspaceID: fixtures.DefaultWorkspaceID,
			service: "cloudflare",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			matched, err := all.memories.MatchMemories(ctx,
				testCase.agentID, testCase.workspaceID, testCase.service, 10)
			if err != nil {
				t.Fatalf("匹配授权记忆失败：%v", err)
			}
			if len(matched) != 0 {
				t.Errorf("记忆溢出到了%s：匹配到 %d 条", testCase.name, len(matched))
			}
		})
	}
}

func TestTrustMemories_Touch_RefreshesLastUsedAt(t *testing.T) {
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	usedAt := fixtures.Instant.Add(time.Hour)
	touched, err := all.memories.Touch(ctx, fixtures.DefaultMemoryID, usedAt)
	if err != nil {
		t.Fatalf("刷新使用时刻失败：%v", err)
	}
	if !touched.LastUsedAt.Equal(usedAt) {
		t.Errorf("使用时刻是 %v，期望 %v", touched.LastUsedAt, usedAt)
	}

	// 失效之后不能再被记为使用过，否则「长期未使用」会被一次误调用重置。
	if _, invalidateErr := all.memories.Invalidate(ctx, fixtures.DefaultMemoryID,
		trust.ReasonUnusedTooLong, fixtures.Instant); invalidateErr != nil {
		t.Fatalf("使记忆失效失败：%v", invalidateErr)
	}
	_, err = all.memories.Touch(ctx, fixtures.DefaultMemoryID, usedAt)
	assertCode(t, err, apperr.CodeConflict)
}

func TestTrustMemories_Invalidate_RequiresAReasonAndHappensOnce(t *testing.T) {
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	// 数据库的 CHECK 也会因为「失效却没有原因」而报同一个错误码，
	// 所以这里还要认那句可执行的说明 —— 少了仓储的守卫，调用方只会看到
	// 一句 constraint failed，不知道该补什么。
	_, err := all.memories.Invalidate(ctx, fixtures.DefaultMemoryID, "", fixtures.Instant)
	assertCode(t, err, apperr.CodeInvalidRequest)
	if !strings.Contains(err.Error(), "必须给出原因") {
		t.Errorf("错误没有说明缺了什么：%v", err)
	}

	if _, invalidateErr := all.memories.Invalidate(ctx, fixtures.DefaultMemoryID,
		trust.ReasonProviderDisconnected, fixtures.Instant); invalidateErr != nil {
		t.Fatalf("使记忆失效失败：%v", invalidateErr)
	}

	// 第二次失效会覆盖首次的原因，那会让「它当初为什么失效」变成另一个答案。
	_, err = all.memories.Invalidate(ctx, fixtures.DefaultMemoryID,
		trust.ReasonUnusedTooLong, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)

	stored, err := all.memories.MemoryByID(ctx, fixtures.DefaultMemoryID)
	if err != nil {
		t.Fatalf("读取记忆失败：%v", err)
	}
	if stored.InvalidationReason != trust.ReasonProviderDisconnected {
		t.Errorf("失效原因被改成了 %q", stored.InvalidationReason)
	}
}

func TestTrustMemories_Delete_ReportsWhenNothingWasRemoved(t *testing.T) {
	// REQ-TRUST-005 AC1：删掉一条记忆后对应请求下次进入审批。
	// 删不到行时必须报错 —— 调用方以为删掉了、实际没删，那条记忆下一次还会放行。
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	if err := all.memories.DeleteMemory(ctx, fixtures.DefaultMemoryID); err != nil {
		t.Fatalf("删除记忆失败：%v", err)
	}

	matched, err := all.memories.MatchMemories(ctx,
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("匹配授权记忆失败：%v", err)
	}
	if len(matched) != 0 {
		t.Errorf("删除后仍匹配到 %d 条", len(matched))
	}

	assertCode(t, all.memories.DeleteMemory(ctx, fixtures.DefaultMemoryID), apperr.CodeNotFound)
}

func TestTrustMemories_ListQueries_RejectNonPositiveLimit(t *testing.T) {
	all := newMemoryChain(t)
	ctx := t.Context()

	for _, limit := range []int{0, -1} {
		_, err := all.memories.MatchMemories(ctx,
			fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)

		_, err = all.memories.MemoriesByStatus(ctx, trust.StatusActive, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestMemoryReferences_RoundTripThroughDecisionsAndLeases(t *testing.T) {
	// PRD §9.6 的 matched_memory 与 §9.7 的 source_memory_id：
	// 未命中记忆时为空，命中后记得住是哪一条。
	ctx := t.Context()
	all := newMemoryChain(t)
	if _, err := all.memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("写入授权记忆失败：%v", err)
	}

	issued := fixtures.Lease()
	issued.SourceMemoryID = fixtures.DefaultMemoryID
	stored, err := all.chain.leases.IssueLease(ctx, issued)
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	if stored.SourceMemoryID != fixtures.DefaultMemoryID {
		t.Errorf("Lease 的来源记忆是 %q", stored.SourceMemoryID)
	}

	// 记忆被 Lease 引用着就删不掉，账本里「这次靠哪条记忆放行」不会失去答案。
	// 报 conflict 而不是 internal：这是当前状态与这次请求不相容，不是网关故障。
	assertCode(t, all.memories.DeleteMemory(ctx, fixtures.DefaultMemoryID), apperr.CodeConflict)

	// 决策侧同样记得住，且未命中时留空。
	existing, err := all.chain.chain.decisions.DecisionByID(ctx, fixtures.DefaultDecisionID)
	if err != nil {
		t.Fatalf("读取决策失败：%v", err)
	}
	if existing.TrustMemoryID != "" {
		t.Errorf("未命中记忆的决策带着 %q", existing.TrustMemoryID)
	}
}

func TestTrustMemories_ThroughTheCoreInterface_Works(t *testing.T) {
	ctx := t.Context()
	all := newMemoryChain(t)

	var memories trust.Repository = all.memories
	if _, err := memories.CreateMemory(ctx, fixtures.Memory()); err != nil {
		t.Fatalf("经接口写入记忆失败：%v", err)
	}

	matched, err := memories.MatchMemories(ctx,
		fixtures.DefaultAgentID, fixtures.DefaultWorkspaceID, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("经接口匹配记忆失败：%v", err)
	}
	if len(matched) != 1 || matched[0].Behavior != trust.BehaviorAutoAllow {
		t.Errorf("经接口匹配到 %d 条", len(matched))
	}

	// 编译期确认新增的两列已经进入领域对象。
	_ = decision.Decision{TrustMemoryID: fixtures.DefaultMemoryID}
	_ = lease.Lease{SourceMemoryID: fixtures.DefaultMemoryID}
}
