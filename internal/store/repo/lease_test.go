package repo_test

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// leaseChain 是审批与 Lease 两个仓储，共用同一个已迁移的数据库。
type leaseChain struct {
	chain     requestChain
	approvals *repo.Approvals
	leases    *repo.Leases
}

// newLeaseChain 把请求、决策都写好，使审批项与 Lease 的外键都有目标。
func newLeaseChain(t *testing.T) leaseChain {
	t.Helper()

	chain := seededDecisionChain(t)
	if _, err := chain.decisions.CreateDecision(t.Context(), fixtures.Decision()); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	return leaseChain{
		chain:     chain,
		approvals: repo.NewApprovals(chain.db),
		leases:    repo.NewLeases(chain.db),
	}
}

// seededLeaseChain 再写入一个待决审批项，使 Lease 可以签发。
func seededLeaseChain(t *testing.T) leaseChain {
	t.Helper()

	all := newLeaseChain(t)
	if _, err := all.approvals.CreateApproval(t.Context(), fixtures.Approval()); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
	return all
}

func TestApprovals_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := newLeaseChain(t)
	want := fixtures.Approval()

	created, err := all.approvals.CreateApproval(ctx, want)
	if err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := all.approvals.ApprovalByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byDecision, err := all.approvals.ApprovalByDecisionID(ctx, want.DecisionID)
	if err != nil {
		t.Fatalf("按决策读取失败：%v", err)
	}
	if byDecision != want {
		t.Errorf("按决策读到 %+v，期望 %+v", byDecision, want)
	}
}

func TestApprovals_Pending_StoresNullActionAndDecidedAt(t *testing.T) {
	// 待决的审批项不能带着结果。零值时间落成 NULL，不冒充「很久以前决出过」。
	ctx := t.Context()
	all := newLeaseChain(t)

	created, err := all.approvals.CreateApproval(ctx, fixtures.Approval())
	if err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
	if created.Action != "" || !created.DecidedAt.IsZero() {
		t.Errorf("待决审批项带着结果：action=%q decided_at=%v", created.Action, created.DecidedAt)
	}

	var action, decidedAt sql.NullString
	if err := all.chain.db.Reader().QueryRowContext(ctx,
		`SELECT action, decided_at FROM approvals WHERE id = ?`,
		fixtures.DefaultApprovalID).Scan(&action, &decidedAt); err != nil {
		t.Fatalf("直接读取审批项失败：%v", err)
	}
	if action.Valid || decidedAt.Valid {
		t.Error("库里存的不是 NULL")
	}
}

func TestApprovals_Settle_WritesActionAndDecidedAtTogether(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)
	decidedAt := fixtures.Instant.Add(time.Minute)

	settled, err := all.approvals.Settle(ctx, fixtures.DefaultApprovalID,
		approval.ActionAllowOnce, approval.StatusApproved, decidedAt)
	if err != nil {
		t.Fatalf("处理审批项失败：%v", err)
	}
	if settled.Action != approval.ActionAllowOnce {
		t.Errorf("操作是 %q，期望 allow_once", settled.Action)
	}
	if settled.Status != approval.StatusApproved {
		t.Errorf("状态是 %q，期望 approved", settled.Status)
	}
	if !settled.DecidedAt.Equal(decidedAt) {
		t.Errorf("决出时刻是 %v，期望 %v", settled.DecidedAt, decidedAt)
	}
}

func TestApprovals_SettleTwice_ReportsConflict(t *testing.T) {
	// 已经处理过的审批项不能被再放行一次。
	ctx := t.Context()
	all := seededLeaseChain(t)

	if _, err := all.approvals.Settle(ctx, fixtures.DefaultApprovalID,
		approval.ActionDeny, approval.StatusRejected, fixtures.Instant); err != nil {
		t.Fatalf("首次处理失败：%v", err)
	}
	_, err := all.approvals.Settle(ctx, fixtures.DefaultApprovalID,
		approval.ActionAllowOnce, approval.StatusApproved, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)
}

func TestApprovals_ConcurrentSettle_SucceedsOnlyOnce(t *testing.T) {
	// 同一个审批项被两个窗口同时决策时，只能有一个成功。
	ctx := t.Context()
	all := seededLeaseChain(t)

	const racers = 8
	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
		succeeded int
	)
	waitGroup.Add(racers)
	for index := range racers {
		go func() {
			defer waitGroup.Done()
			action, status := approval.ActionAllowOnce, approval.StatusApproved
			if index%2 == 1 {
				action, status = approval.ActionDeny, approval.StatusRejected
			}
			if _, err := all.approvals.Settle(ctx, fixtures.DefaultApprovalID,
				action, status, fixtures.Instant); err == nil {
				mutex.Lock()
				succeeded++
				mutex.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if succeeded != 1 {
		t.Errorf("%d 个并发决策中有 %d 个成功，期望恰好 1 个", racers, succeeded)
	}
}

func TestApprovals_PendingDueBefore_ListsOnlyOverdueOnes(t *testing.T) {
	ctx := t.Context()
	all := newLeaseChain(t)

	overdue := fixtures.Approval(fixtures.WithApprovalExpiresAt(fixtures.Instant.Add(time.Minute)))
	if _, err := all.approvals.CreateApproval(ctx, overdue); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}

	nothing, err := all.approvals.PendingApprovalsDueBefore(ctx, fixtures.Instant, 10)
	if err != nil {
		t.Fatalf("列出超时审批项失败：%v", err)
	}
	if len(nothing) != 0 {
		t.Errorf("尚未到点就列出了 %d 条", len(nothing))
	}

	due, err := all.approvals.PendingApprovalsDueBefore(ctx, fixtures.Instant.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("列出超时审批项失败：%v", err)
	}
	if len(due) != 1 || due[0].ID != overdue.ID {
		t.Errorf("到点后列出了 %d 条，期望 1 条", len(due))
	}

	// 已决的审批项不再参与超时清扫。
	if _, settleErr := all.approvals.Settle(ctx, overdue.ID,
		approval.ActionDeny, approval.StatusRejected, fixtures.Instant); settleErr != nil {
		t.Fatalf("处理审批项失败：%v", settleErr)
	}
	settled, err := all.approvals.PendingApprovalsDueBefore(ctx, fixtures.Instant.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("列出超时审批项失败：%v", err)
	}
	if len(settled) != 0 {
		t.Errorf("已决的审批项仍被列入超时清扫：%d 条", len(settled))
	}
}

func TestApprovals_ListQueries_RejectNonPositiveLimit(t *testing.T) {
	all := newLeaseChain(t)
	ctx := t.Context()

	for _, limit := range []int{0, -1} {
		_, err := all.approvals.ApprovalsByStatus(ctx, approval.StatusPending, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)

		_, err = all.approvals.PendingApprovalsDueBefore(ctx, fixtures.Instant, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestLeases_IssueThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)
	want := fixtures.Lease()

	issued, err := all.leases.IssueLease(ctx, want)
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	if issued != want {
		t.Errorf("签发返回 %+v，期望 %+v", issued, want)
	}

	byID, err := all.leases.LeaseByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestLeases_Unlimited_RoundTripsAsNull(t *testing.T) {
	// 不限次数存成 NULL：0 不能当成一个真实上限，那会让这条 Lease 一次都用不了。
	ctx := t.Context()
	all := seededLeaseChain(t)

	issued, err := all.leases.IssueLease(ctx,
		fixtures.Lease(fixtures.WithLeaseRequestLimit(lease.Unlimited)))
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	if issued.RequestLimit != lease.Unlimited {
		t.Errorf("次数上限是 %d，期望不限次数", issued.RequestLimit)
	}

	var stored sql.NullInt64
	if err := all.chain.db.Reader().QueryRowContext(ctx,
		`SELECT request_limit FROM leases WHERE id = ?`,
		fixtures.DefaultLeaseID).Scan(&stored); err != nil {
		t.Fatalf("直接读取次数上限失败：%v", err)
	}
	if stored.Valid {
		t.Errorf("库里存的是 %d，期望 NULL", stored.Int64)
	}
}

func TestLeases_SecondLeaseForTheSameApproval_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)

	if _, err := all.leases.IssueLease(ctx, fixtures.Lease()); err != nil {
		t.Fatalf("首次签发失败：%v", err)
	}
	_, err := all.leases.IssueLease(ctx,
		fixtures.Lease(fixtures.WithLeaseID("01K1LEASE00000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestLeases_Consume_StopsAtTheRequestLimit(t *testing.T) {
	// REQ-LEASE-001 AC3 的存储层前提：用满之后再也拿不到这条 Lease。
	ctx := t.Context()
	all := seededLeaseChain(t)
	if _, err := all.leases.IssueLease(ctx, fixtures.Lease(fixtures.WithLeaseRequestLimit(2))); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	for used := 1; used <= 2; used++ {
		consumed, err := all.leases.Consume(ctx, fixtures.DefaultLeaseID, fixtures.Instant)
		if err != nil {
			t.Fatalf("第 %d 次使用失败：%v", used, err)
		}
		if consumed.UsedRequests != used {
			t.Errorf("第 %d 次使用后计数为 %d", used, consumed.UsedRequests)
		}
	}

	_, err := all.leases.Consume(ctx, fixtures.DefaultLeaseID, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)
}

func TestLeases_Consume_RefusesExpiredLease(t *testing.T) {
	// 到期的 Lease 不能再被使用，哪怕状态还没被清扫任务改过来。
	ctx := t.Context()
	all := seededLeaseChain(t)
	if _, err := all.leases.IssueLease(ctx, fixtures.Lease()); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	_, err := all.leases.Consume(ctx, fixtures.DefaultLeaseID, fixtures.Instant.Add(time.Hour))
	assertCode(t, err, apperr.CodeConflict)
}

func TestLeases_ConcurrentConsume_DoesNotOverIssue(t *testing.T) {
	// 并发递增不超发：条件判定与递增在同一条语句里完成。
	// 先读后写会让两个并发请求都读到同一个未达上限的计数，于是各自加一。
	ctx := t.Context()
	all := seededLeaseChain(t)

	const limit = 3
	if _, err := all.leases.IssueLease(ctx, fixtures.Lease(fixtures.WithLeaseRequestLimit(limit))); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	const racers = 12
	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
		succeeded int
	)
	waitGroup.Add(racers)
	for range racers {
		go func() {
			defer waitGroup.Done()
			if _, err := all.leases.Consume(ctx, fixtures.DefaultLeaseID, fixtures.Instant); err == nil {
				mutex.Lock()
				succeeded++
				mutex.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if succeeded != limit {
		t.Errorf("%d 个并发请求中有 %d 个成功，期望恰好 %d 个", racers, succeeded, limit)
	}

	final, err := all.leases.LeaseByID(ctx, fixtures.DefaultLeaseID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if final.UsedRequests != limit {
		t.Errorf("最终计数为 %d，期望 %d", final.UsedRequests, limit)
	}
}

func TestLeases_Shorten_RefusesToExtend(t *testing.T) {
	// REQ-LEASE-002：支持缩短，但这个方法不能被用来延长授权。
	ctx := t.Context()
	all := seededLeaseChain(t)
	original := fixtures.Lease()
	if _, err := all.leases.IssueLease(ctx, original); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	earlier := original.ExpiresAt.Add(-5 * time.Minute)
	shortened, err := all.leases.Shorten(ctx, original.ID, earlier, fixtures.Instant)
	if err != nil {
		t.Fatalf("缩短 Lease 失败：%v", err)
	}
	if !shortened.ExpiresAt.Equal(earlier) {
		t.Errorf("缩短后到期时刻为 %v，期望 %v", shortened.ExpiresAt, earlier)
	}

	for _, attempt := range []time.Time{earlier.Add(time.Minute), earlier} {
		_, extendErr := all.leases.Shorten(ctx, original.ID, attempt, fixtures.Instant)
		assertCode(t, extendErr, apperr.CodeInvalidRequest)
	}

	unchanged, err := all.leases.LeaseByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if !unchanged.ExpiresAt.Equal(earlier) {
		t.Errorf("到期时刻被改成了 %v", unchanged.ExpiresAt)
	}
}

func TestLeases_Close_OnlyLeavesTheActiveState(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)
	if _, err := all.leases.IssueLease(ctx, fixtures.Lease()); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	// 关闭为 active 没有意义，这个入口不接受它。
	_, err := all.leases.Close(ctx, fixtures.DefaultLeaseID, lease.StatusActive, fixtures.Instant)
	assertCode(t, err, apperr.CodeInvalidRequest)

	revoked, err := all.leases.Close(ctx, fixtures.DefaultLeaseID, lease.StatusRevoked, fixtures.Instant)
	if err != nil {
		t.Fatalf("撤销 Lease 失败：%v", err)
	}
	if revoked.Status != lease.StatusRevoked {
		t.Errorf("撤销后状态是 %q", revoked.Status)
	}

	// 已经关闭的 Lease 不能被再次关闭，也不能被使用或缩短。
	_, err = all.leases.Close(ctx, fixtures.DefaultLeaseID, lease.StatusExpired, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)

	_, err = all.leases.Consume(ctx, fixtures.DefaultLeaseID, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)

	_, err = all.leases.Shorten(ctx, fixtures.DefaultLeaseID, fixtures.Instant.Add(time.Minute), fixtures.Instant)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestLeases_ActiveDueBefore_ListsOnlyExpiredActiveOnes(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)
	if _, err := all.leases.IssueLease(ctx, fixtures.Lease()); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	nothing, err := all.leases.ActiveLeasesDueBefore(ctx, fixtures.Instant, 10)
	if err != nil {
		t.Fatalf("列出到期 Lease 失败：%v", err)
	}
	if len(nothing) != 0 {
		t.Errorf("尚未到期就列出了 %d 条", len(nothing))
	}

	due, err := all.leases.ActiveLeasesDueBefore(ctx, fixtures.Instant.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("列出到期 Lease 失败：%v", err)
	}
	if len(due) != 1 {
		t.Fatalf("到期后列出了 %d 条，期望 1 条", len(due))
	}

	active, err := all.leases.LeasesByStatus(ctx, lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出生效中的 Lease 失败：%v", err)
	}
	if len(active) != 1 {
		t.Errorf("生效中的 Lease 有 %d 条，期望 1 条", len(active))
	}
}

func TestLeases_ListQueries_RejectNonPositiveLimit(t *testing.T) {
	all := seededLeaseChain(t)
	ctx := t.Context()

	for _, limit := range []int{0, -1} {
		_, err := all.leases.LeasesByStatus(ctx, lease.StatusActive, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)

		_, err = all.leases.ActiveLeasesDueBefore(ctx, fixtures.Instant, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestLeases_UnknownIdentity_ReportsInvalidRequest(t *testing.T) {
	all := seededLeaseChain(t)

	issued := fixtures.Lease()
	issued.IdentityID = "01K1MISSING00000000000000"
	_, err := all.leases.IssueLease(t.Context(), issued)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestLeaseChain_ThroughTheCoreInterfaces_Works(t *testing.T) {
	ctx := t.Context()
	all := seededLeaseChain(t)

	var (
		approvals approval.Repository = all.approvals
		leases    lease.Repository    = all.leases
	)

	if _, err := leases.IssueLease(ctx, fixtures.Lease()); err != nil {
		t.Fatalf("经接口签发 Lease 失败：%v", err)
	}
	settled, err := approvals.Settle(ctx, fixtures.DefaultApprovalID,
		approval.ActionAllowOnce, approval.StatusApproved, fixtures.Instant)
	if err != nil {
		t.Fatalf("经接口处理审批项失败：%v", err)
	}
	if settled.Status != approval.StatusApproved {
		t.Errorf("经接口读到的状态是 %q", settled.Status)
	}
}
