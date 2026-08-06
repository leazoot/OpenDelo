package pipeline_test

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/audit"
)

/*
 * 等不到人的审批（REQ-CAP-003）。
 *
 * 这条路此前完全不生效：`approval.Manager.Expire` 写好了也测过，但整个二进制里
 * 没有任何东西叫它 —— 一条请求可以在缝前挂几小时后仍然被批准
 * （2026-08-04 人工验收实测 7.5 小时后仍可放行）。清扫的接线在 `internal/cli`，
 * 这里守的是它每一轮该做什么。
 */

func TestExpireApprovals_ClosesTheOnesNobodyAnswered_AndLeavesALedgerEntry(t *testing.T) {
	all := newHarness(t)
	result := handle(t, all, inputs(t, writeCall()))
	if result.Approval == nil {
		t.Fatal("这次请求没有停在缝前，用例测不到超时")
	}

	// 请求停在缝前时已经写过一条 decision.denied（那是「这次不放行」），
	// 因此只看清扫**新增**的那些 —— 拿整份账本比对会把它算进来。
	before := len(eventTypes(t, all))

	// 走过时限：审批项的 expires_at 是决策那一刻加上超时时长。
	all.clock.Advance(time.Hour)

	closed, err := all.pipeline.ExpireApprovals(t.Context(), 10)
	if err != nil {
		t.Fatalf("关闭超时审批失败：%v", err)
	}
	if closed != 1 {
		t.Fatalf("关闭了 %d 条，期望 1 条", closed)
	}

	// AC3：账本上留得下痕迹，而且是「等不到人」而不是「用户拒绝」。
	types := eventTypes(t, all)
	added := types[:len(types)-before]
	if len(added) != 1 {
		t.Fatalf("清扫写了 %d 条账，期望 1 条：%v", len(added), added)
	}
	if added[0] != audit.EventApprovalExpired {
		t.Errorf("清扫写下的是 %q，期望 approval.expired —— 「你拒绝了」与「你没看见」不该长成一个样子",
			added[0])
	}

	// AC2：超时不产生 Lease。
	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != 0 {
		t.Errorf("超时之后仍有 %d 条生效的 Lease", len(issued))
	}
}

// TestExpireApprovals_BeforeTheDeadline_TouchesNothing：还没到时限的不能被关掉。
// 少了这一条，一个「把所有待审批都关掉」的实现也能让上面那条通过。
func TestExpireApprovals_BeforeTheDeadline_TouchesNothing(t *testing.T) {
	all := newHarness(t)
	if handle(t, all, inputs(t, writeCall())).Approval == nil {
		t.Fatal("这次请求没有停在缝前，用例测不到超时")
	}

	closed, err := all.pipeline.ExpireApprovals(t.Context(), 10)
	if err != nil {
		t.Fatalf("关闭超时审批失败：%v", err)
	}
	if closed != 0 {
		t.Fatalf("时限还没到就关掉了 %d 条", closed)
	}
	if types := eventTypes(t, all); contains(types, audit.EventApprovalExpired) {
		t.Errorf("时限还没到就写了 approval.expired：%v", types)
	}
}

// TestExpireApprovals_AnAlreadyDecidedApproval_IsLeftAlone：已经有人决定过的
// 不该被清扫碰到，否则账本上会出现「先放行、后超时」这种说不通的顺序。
func TestExpireApprovals_AnAlreadyDecidedApproval_IsLeftAlone(t *testing.T) {
	all := newHarness(t)
	result := handle(t, all, inputs(t, writeCall()))
	if result.Approval == nil {
		t.Fatal("这次请求没有停在缝前，用例测不到超时")
	}

	settled, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: result.Approval.ID, Action: approval.ActionDeny,
	})
	if err != nil {
		t.Fatalf("拒绝失败：%v", err)
	}
	if settled.Approval.Status == "" {
		t.Fatal("审批项没有被决定")
	}

	all.clock.Advance(time.Hour)

	closed, err := all.pipeline.ExpireApprovals(t.Context(), 10)
	if err != nil {
		t.Fatalf("关闭超时审批失败：%v", err)
	}
	if closed != 0 {
		t.Errorf("已经决定过的审批被清扫关了 %d 条", closed)
	}
}

func contains(all []audit.EventType, wanted audit.EventType) bool {
	for _, each := range all {
		if each == wanted {
			return true
		}
	}
	return false
}

/*
 * 窗口已经过去时的放行（回归，R-43）。
 *
 * 收敛出来的时间窗在**决策那一刻**算（默认 15 分钟），而人可以隔更久才点头。
 * 原先的顺序是「Settle → 写审计 → 签 Lease」：窗口过期时 `lease.Issue` 正确拒签，
 * 但审批项已被消费、账本上已经写着「用户放行」—— 那件事并没有发生，而这条请求
 * 从此走重放分支拿到 409，永远批不出来。
 *
 * 2026-08-04 人工验收撞出：一条请求在缝前挂了 7 小时 12 分钟。
 */
func TestSettleApproval_AfterTheGrantedWindowHasPassed_ChangesNothing_Regression(t *testing.T) {
	all := newHarness(t)
	result := handle(t, all, inputs(t, writeCall()))
	if result.Approval == nil {
		t.Fatal("这次请求没有停在缝前，用例测不到过期窗口")
	}
	before := len(eventTypes(t, all))

	// 走过收敛出来的时间窗，但**不跑清扫** —— 模拟「清扫还没轮到它」那一瞬间。
	all.clock.Advance(time.Hour)

	_, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: result.Approval.ID, Action: approval.ActionAllowUntilTaskEnd,
		Confirmations: 1,
	})
	if err == nil {
		t.Fatal("窗口已经过去却放行成功了")
	}

	// 一个字都不该写：账本上不能出现一件没有发生的事。
	if added := len(eventTypes(t, all)) - before; added != 0 {
		t.Errorf("失败的放行仍然写了 %d 条账", added)
	}

	// 审批项不能被消费：消费掉之后重试会走重放分支，这条请求就永远批不出来了。
	waiting, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: result.Approval.ID, Action: approval.ActionDeny, Confirmations: 1,
	})
	if err != nil {
		t.Fatalf("审批项已经被消费掉了，拒绝也做不了：%v", err)
	}
	if waiting.Approval.Action != approval.ActionDeny {
		t.Errorf("重试之后的动作是 %q，期望 deny —— 说明第一次已经把它消费掉了",
			waiting.Approval.Action)
	}
}

// TestSettleApproval_AfterTheWindowHasPassed_DenyStillWorks：那道检查只挡放行。
// 挡住拒绝的话，一条过期的请求会连「明确不要」都做不到，只能等清扫来关 ——
// 而用户此刻正看着它。
func TestSettleApproval_AfterTheWindowHasPassed_DenyStillWorks(t *testing.T) {
	all := newHarness(t)
	result := handle(t, all, inputs(t, writeCall()))
	if result.Approval == nil {
		t.Fatal("这次请求没有停在缝前，用例测不到过期窗口")
	}

	all.clock.Advance(time.Hour)

	denied, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: result.Approval.ID, Action: approval.ActionDeny, Confirmations: 1,
	})
	if err != nil {
		t.Fatalf("窗口过去之后拒绝不掉：%v", err)
	}
	if denied.Approval.Action != approval.ActionDeny {
		t.Errorf("落地的动作是 %q，期望 deny", denied.Approval.Action)
	}
}
