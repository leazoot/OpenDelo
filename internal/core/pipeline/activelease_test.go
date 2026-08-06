package pipeline_test

import (
	"testing"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 已签发的授权参与后续决策（D-17，关闭 R-39）。
 *
 * `core/decision` 的用例证明了「命中就放行」，这里证明的是另一半：
 * **命中之后复用那一条，不另签**。为同一次请求签出第二条授权，等于把一次人工
 * 确认换成两份权限，计次上限也随之翻倍 —— 而这一点只有在链路上才看得见。
 */

// again 造第二次同样的调用。
//
// 请求主键必须换一个：`decisions.capability_request_id` 是唯一索引，同一条请求
// 只允许有一次决策 —— 那正是 REQ-API-004 的幂等在数据库层面的落点。
// 现实里第二次调用本来就是另一条能力请求。
func again(t *testing.T, all harness, id string) pipeline.Inputs {
	t.Helper()

	if _, err := all.requests.CreateRequest(
		t.Context(), fixtures.Request(fixtures.WithRequestID(id)),
	); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	in := inputs(t, writeCall())
	in.Request.ID = id
	return in
}

func activeLeases(t *testing.T, all harness) []lease.Lease {
	t.Helper()

	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	return issued
}

func TestHandle_CoveredByAnIssuedLease_ReusesItInsteadOfIssuingASecond(t *testing.T) {
	all := newHarness(t)
	item := pending(t, all)

	// 「允许到任务结束」签一条会话绑定的授权，且**不生成记忆**。
	granted := settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)
	if granted.Lease == nil {
		t.Fatal("批准之后没有签发 Lease")
	}
	first := granted.Lease.ID

	// 同一件事再来一次：此前会被再问一遍人。
	result := handle(t, all, again(t, all, "01K1REQUEST00000000SECOND"))

	if result.Outcome.Verdict != decision.VerdictAutoAllow {
		t.Fatalf("结论为 %s，期望 auto_allow（原因 %s）", result.Outcome.Verdict, result.Outcome.Reason)
	}
	if result.Outcome.Reason != decision.ReasonActiveLease {
		t.Errorf("原因为 %s，期望 active_lease", result.Outcome.Reason)
	}
	if result.Approval != nil {
		t.Error("已经批准过的同一件事又开了一次审批")
	}
	if result.Lease == nil {
		t.Fatal("放行了却没有带出授权")
	}
	if result.Lease.ID != first {
		t.Errorf("放行用的是 Lease %q，期望复用 %q —— 又签了一条", result.Lease.ID, first)
	}

	// 库里一共只该有那一条。计次发生在执行前的那一句 Use 上，链路本身不消耗，
	// 因此这里数的是条数而不是余量：真要是另签了一条，这里会看到两条。
	remaining := activeLeases(t, all)
	if len(remaining) != 1 {
		t.Fatalf("库里有 %d 条活跃授权，期望 1 条 —— 一次确认换出了两份权限", len(remaining))
	}
	if remaining[0].ID != first {
		t.Errorf("活着的那条是 %q，不是原来那条 %q", remaining[0].ID, first)
	}
}

func TestHandle_IssuedLeaseOnAnotherResource_StillWaitsForAPerson(t *testing.T) {
	// 差一维就是不覆盖。授权罩得住的只有它签发时收敛出的那个范围。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	in := again(t, all, "01K1REQUEST00000000ELSEWH")
	in.Call.Resource = `{"repo":"Runcoor/another"}`
	in.Request.Resource = `{"repo":"Runcoor/another"}`
	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，期望 require_approval —— 一条授权罩住了它范围之外的资源",
			result.Outcome.Verdict)
	}
	if result.Lease != nil {
		t.Errorf("范围之外的请求拿到了授权 %q", result.Lease.ID)
	}
}

func TestHandle_ExpiredLease_DoesNotCoverAnything(t *testing.T) {
	// 所有授权默认有期限（不可协商约束第 5 条）。过期的那一条不该再罩住任何东西 ——
	// 而决策引擎不做 I/O、判不了「现在几点」，这一条由装配输入的那一步保证。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	all.clock.Advance(scope.DefaultDuration + 1)

	result := handle(t, all, again(t, all, "01K1REQUEST00000000EXPIRD"))

	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，期望 require_approval —— 过期的授权仍然放行了",
			result.Outcome.Verdict)
	}
}
