package pipeline_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 身份断开的级联（REQ-IDENT-001 AC2）。
 */

const operationID = "01J0000000000000000OPID"

func TestDisconnectIdentity_RevokesLeasesAndInvalidatesMemoriesAndPausesTheIdentity(t *testing.T) {
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAutoAllowInProject)

	revocation, err := all.pipeline.DisconnectIdentity(
		t.Context(), fixtures.DefaultIdentityID, operationID)
	if err != nil {
		t.Fatalf("断开身份失败：%v", err)
	}

	if len(revocation.RevokedLeases) != 1 {
		t.Errorf("收回了 %d 条 Lease，期望 1 条", len(revocation.RevokedLeases))
	}
	if len(revocation.InvalidatedMemories) != 1 {
		t.Errorf("失效了 %d 条记忆，期望 1 条", len(revocation.InvalidatedMemories))
	}
	if revocation.Identity.Status != matcher.StatusNeedsReview {
		t.Errorf("身份状态为 %s，断开之后不该还能自动使用", revocation.Identity.Status)
	}

	assertNoLease(t, all)
	assertHasEvent(t, all, audit.EventLeaseRevoked)

	// 失效的记忆读得到而不是消失，且带着原因（REQ-TRUST-004 AC2）。
	memories := repo.NewTrustMemories(all.db)
	invalidated, err := memories.MemoriesByStatus(t.Context(), trust.StatusInvalidated, 10)
	if err != nil {
		t.Fatalf("列出失效记忆失败：%v", err)
	}
	if len(invalidated) != 1 || invalidated[0].InvalidationReason == "" {
		t.Fatalf("失效记忆为 %v，期望 1 条且带着原因", invalidated)
	}
}

func TestDisconnectIdentity_LedgerWriteFails_LeavesTheLeaseActive(t *testing.T) {
	// 顺序是先记账再收回：账本写不进去时那条 Lease 必须保持原样，
	// 否则账本上会缺一段「这条授权是什么时候没的」。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAllowOnce)

	broken := rebuildWithAudit(t, all, failingAudit{err: errLedgerDown})
	if _, err := broken.DisconnectIdentity(
		t.Context(), fixtures.DefaultIdentityID, operationID); err == nil {
		t.Fatal("账本写不进去却断开成功了")
	}

	assertLeaseTotal(t, all, 1)

	still, err := repo.NewIdentities(all.db).IdentityByID(
		t.Context(), fixtures.DefaultIdentityID)
	if err != nil {
		t.Fatalf("读取身份失败：%v", err)
	}
	if still.Status != matcher.StatusOK {
		t.Errorf("身份状态已经改成了 %s，而它名下的 Lease 还活着", still.Status)
	}
}

func TestDisconnectIdentity_WithNothingIssued_IsStillRecordedAsPaused(t *testing.T) {
	// 名下没有任何授权时也要把身份停下来：REQ-IDENT-001 AC2 的级联为空
	// 不等于这次断开没发生。
	all := newHarness(t)

	revocation, err := all.pipeline.DisconnectIdentity(
		t.Context(), fixtures.DefaultIdentityID, operationID)
	if err != nil {
		t.Fatalf("断开身份失败：%v", err)
	}
	if len(revocation.RevokedLeases) != 0 || len(revocation.InvalidatedMemories) != 0 {
		t.Errorf("凭空收回了东西：%v / %v",
			revocation.RevokedLeases, revocation.InvalidatedMemories)
	}
	if revocation.Identity.Status != matcher.StatusNeedsReview {
		t.Errorf("身份状态为 %s", revocation.Identity.Status)
	}
}

func TestDisconnectIdentity_MissingArgumentsAreRefused(t *testing.T) {
	all := newHarness(t)

	cases := map[string]struct{ identityID, operationID string }{
		"没给身份":            {identityID: "", operationID: operationID},
		"没给 operation_id": {identityID: fixtures.DefaultIdentityID, operationID: ""},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := all.pipeline.DisconnectIdentity(
				t.Context(), testCase.identityID, testCase.operationID)
			if !apperr.Is(err, apperr.CodeInvalidRequest) {
				t.Errorf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
			}
		})
	}
}

func TestDisconnectIdentity_UnknownIdentityIsNotFound(t *testing.T) {
	all := newHarness(t)

	_, err := all.pipeline.DisconnectIdentity(
		t.Context(), "01J000000000000000NOPE", operationID)
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Errorf("错误码为 %s，期望 not_found（%v）", apperr.CodeOf(err), err)
	}
}

func TestIdentities_ListsAndReadsBack(t *testing.T) {
	all := newHarness(t)

	listed, err := all.pipeline.Identities(t.Context(), 10)
	if err != nil {
		t.Fatalf("列出身份失败：%v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("列出了 %d 个身份，期望 1 个", len(listed))
	}

	one, err := all.pipeline.Identity(t.Context(), listed[0].ID)
	if err != nil {
		t.Fatalf("读取身份失败：%v", err)
	}
	if one.ID != listed[0].ID {
		t.Errorf("读回的是 %s，期望 %s", one.ID, listed[0].ID)
	}
}

// store/repo 的实现必须满足本包声明的最小接口 —— 少一个方法在编译期就报出来。
var _ pipeline.IdentityRepository = (*repo.Identities)(nil)
