package pipeline_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 审批落地（REQ-APPROVAL-002、REQ-API-004）。
 *
 * 待审批项由真实链路产出：手写一条进库测不出「决策记录里的风险等级
 * 决定了哪些操作可选」这件事。
 */

// pending 跑一次写操作，让它停在等待人工确认。
func pending(t *testing.T, all harness) approval.Approval {
	t.Helper()

	result := handle(t, all, inputs(t, writeCall()))
	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，这条用例需要一个等待人工确认的请求", result.Outcome.Verdict)
	}
	return *result.Approval
}

func settle(t *testing.T, all harness, id string, action approval.Action) pipeline.SettleResult {
	t.Helper()

	result, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: id, Action: action, Confirmations: 1,
	})
	if err != nil {
		t.Fatalf("审批落地失败：%v", err)
	}
	return result
}

func TestSettleApproval_AllowOnce_IssuesOneLeaseAndAdvancesTheRequest(t *testing.T) {
	all := newHarness(t)
	item := pending(t, all)

	result := settle(t, all, item.ID, approval.ActionAllowOnce)

	if result.Lease == nil {
		t.Fatal("放行之后没有签发 Lease")
	}
	if result.Lease.RequestLimit != 1 {
		t.Errorf("次数上限为 %d，「仅允许这一次」要求 1", result.Lease.RequestLimit)
	}
	if result.Lease.ApprovalID != item.ID {
		t.Errorf("Lease 挂在审批项 %q 上，期望 %q", result.Lease.ApprovalID, item.ID)
	}
	if result.Request.Status != pipeline.StatusApproved {
		t.Errorf("请求状态为 %s，期望 approved", result.Request.Status)
	}
	if result.Memory != nil {
		t.Error("「仅允许这一次」学了一条记忆")
	}
	assertHasEvent(t, all, audit.EventUserAllowed)
}

func TestSettleApproval_Deny_IssuesNothingAndRejectsTheRequest(t *testing.T) {
	all := newHarness(t)
	item := pending(t, all)

	result := settle(t, all, item.ID, approval.ActionDeny)

	if result.Lease != nil {
		t.Error("拒绝之后仍然签发了 Lease")
	}
	if result.Request.Status != pipeline.StatusRejected {
		t.Errorf("请求状态为 %s，期望 rejected", result.Request.Status)
	}
	if result.Approval.Status != approval.StatusRejected {
		t.Errorf("审批状态为 %s，期望 rejected", result.Approval.Status)
	}
	assertNoLease(t, all)
}

func TestSettleApproval_AllowProject_LearnsOneMemoryWithinTheApprovedScope(t *testing.T) {
	// REQ-TRUST-002：记忆不得扩大任何一维。收敛校验在 trust.Manager 里做，
	// 这里断言 pipeline 交给它的确实是那次审批的范围。
	all := newHarness(t)
	item := pending(t, all)

	result := settle(t, all, item.ID, approval.ActionAutoAllowInProject)

	if result.Memory == nil {
		t.Fatal("「今后自动允许」没有学到记忆")
	}
	if result.Memory.Behavior != "auto_allow" {
		t.Errorf("记忆的行为是 %s", result.Memory.Behavior)
	}
	if result.Lease == nil {
		t.Error("这次审批本身没有放行")
	}
}

func TestSettleApproval_RepeatedSameAction_ReturnsTheFirstLeaseAndAuditsOnce(t *testing.T) {
	// REQ-API-004 AC1：连续三次只产生一个 Lease，账本上也只有一条放行记录。
	all := newHarness(t)
	item := pending(t, all)

	first := settle(t, all, item.ID, approval.ActionAllowOnce)
	for attempt := 2; attempt <= 3; attempt++ {
		again := settle(t, all, item.ID, approval.ActionAllowOnce)
		if !again.Replayed {
			t.Errorf("第 %d 次提交没有被标记为重放", attempt)
		}
		if again.Lease == nil || again.Lease.ID != first.Lease.ID {
			t.Errorf("第 %d 次拿到的不是首次那条 Lease", attempt)
		}
	}

	assertEventCount(t, all, audit.EventUserAllowed, 1)
	assertLeaseTotal(t, all, 1)
}

func TestSettleApproval_AfterDeny_AllowingIsRefusedAndNothingIsIssued(t *testing.T) {
	// REQ-API-004 AC2。
	all := newHarness(t)
	item := pending(t, all)
	settle(t, all, item.ID, approval.ActionDeny)

	_, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce, Confirmations: 1,
	})
	if err == nil {
		t.Fatal("拒绝之后仍然放行成功")
	}
	if !apperr.Is(err, apperr.CodeConflict) {
		t.Errorf("错误码为 %s，期望 conflict", apperr.CodeOf(err))
	}
	assertNoLease(t, all)
}

func TestSettleApproval_LedgerWriteFails_NoLeaseIsIssued(t *testing.T) {
	// ADR-004：审计写在放行之前，写不进去就不放行。
	// 先用真实账本产出待审批项，再把账本换成写不进去的那个。
	all := newHarness(t)
	item := pending(t, all)

	broken := rebuildWithAudit(t, all, failingAudit{err: errLedgerDown})
	_, err := broken.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce, Confirmations: 1,
	})
	if err == nil {
		t.Fatal("账本写不进去却放行成功了")
	}
	assertNoLease(t, all)
}

func TestSettleApproval_UnknownApproval_IsNotFound(t *testing.T) {
	all := newHarness(t)

	_, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: "01J0000000000000000NOPE", Action: approval.ActionDeny, Confirmations: 1,
	})
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Errorf("错误码为 %s，期望 not_found（%v）", apperr.CodeOf(err), err)
	}
}

func TestSettleApproval_NotEnoughConfirmations_IsRefused(t *testing.T) {
	// 放行至少要有人点过一次头。
	all := newHarness(t)
	item := pending(t, all)

	_, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce, Confirmations: 0,
	})
	if err == nil {
		t.Fatal("一次确认都没有却放行成功")
	}
	assertNoLease(t, all)
}

// ——— 辅助 ———

func assertNoLease(t *testing.T, all harness) {
	t.Helper()
	assertLeaseTotal(t, all, 0)
}

func assertLeaseTotal(t *testing.T, all harness, expected int) {
	t.Helper()

	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 100)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != expected {
		t.Errorf("库里有 %d 条生效中的 Lease，期望 %d 条", len(issued), expected)
	}
}

func assertHasEvent(t *testing.T, all harness, wanted audit.EventType) {
	t.Helper()

	for _, found := range eventTypes(t, all) {
		if found == wanted {
			return
		}
	}
	t.Errorf("账本里没有 %s 事件，实际有 %v", wanted, eventTypes(t, all))
}

func assertEventCount(t *testing.T, all harness, wanted audit.EventType, expected int) {
	t.Helper()

	found := 0
	for _, event := range eventTypes(t, all) {
		if event == wanted {
			found++
		}
	}
	if found != expected {
		t.Errorf("账本里有 %d 条 %s 事件，期望 %d 条", found, wanted, expected)
	}
}

func TestSettleApproval_ApprovedButNoLeaseWasEverIssued_IsAConflict(t *testing.T) {
	// 首次提交在「审计已写、Lease 还没签」之间中断时，库里会留下一个
	// 状态为 approved 却没有 Lease 的审批项。重放不能替它补签一条 ——
	// 补签会绕开「审计先于放行」那道顺序，所以这里只能报冲突。
	all := newHarness(t)
	item := pending(t, all)

	approvals := repo.NewApprovals(all.db)
	if _, err := approvals.Settle(t.Context(), item.ID,
		approval.ActionAllowOnce, approval.StatusApproved, fixtures.Instant); err != nil {
		t.Fatalf("直接落定审批项失败：%v", err)
	}

	_, err := all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce, Confirmations: 1,
	})
	if !apperr.Is(err, apperr.CodeConflict) {
		t.Errorf("错误码为 %s，期望 conflict（%v）", apperr.CodeOf(err), err)
	}
	assertNoLease(t, all)
}

func TestSettleApproval_DecisionScopeUnreadable_IssuesNothing(t *testing.T) {
	// 读不懂自己写下的范围时，唯一安全的结论是不签发：一条说不清覆盖什么的
	// 授权事后也校验不了请求在不在其中（Fail Closed）。
	all := newHarness(t)

	// 夹具里的 resolved_scope 只有两维，十维不全 —— 正是「读得出 JSON 却
	// 说不清覆盖什么」的那种记录。
	broken := fixtures.Decision()
	if _, err := repo.NewDecisions(all.db).CreateDecision(t.Context(), broken); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	item, err := approvalManagerOf(t, all).Open(t.Context(), broken.ID)
	if err != nil {
		t.Fatalf("创建审批项失败：%v", err)
	}

	_, err = all.pipeline.SettleApproval(t.Context(), pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce, Confirmations: 1,
	})
	if err == nil {
		t.Fatal("范围读不懂却放行成功了")
	}
	assertNoLease(t, all)
	// 停在读范围那一步，账本上不会留下一条「用户放行」——
	// 只靠签发那一层兜底的话，这条事实已经写进去了。
	assertEventCount(t, all, audit.EventUserAllowed, 0)
}

func TestRecordSecretRequestBlocked_WritesASecurityEventToTheLedger(t *testing.T) {
	// REQ-CAP-001 AC2：Agent 直接索取凭据时账本上必须留下痕迹。
	all := newHarness(t)

	if err := all.pipeline.RecordSecretRequestBlocked(t.Context(), pipeline.BlockedRequest{
		OperationID: "01J0000000000000000OPID",
		AgentID:     fixtures.DefaultAgentID,
		WorkspaceID: fixtures.DefaultWorkspaceID,
		Service:     "github",
		Operation:   "credential.read",
	}); err != nil {
		t.Fatalf("记录安全事件失败：%v", err)
	}
	assertHasEvent(t, all, audit.EventSecretRequestBlocked)
}

func TestRecordSecretRequestBlocked_LedgerWriteFails_IsPassedUp(t *testing.T) {
	// 账本写不进去时整件事都要失败，而不是拒绝完就算了（ADR-004）。
	all := newHarness(t)
	broken := rebuildWithAudit(t, all, failingAudit{err: errLedgerDown})

	if err := broken.RecordSecretRequestBlocked(t.Context(), pipeline.BlockedRequest{
		OperationID: "01J0000000000000000OPID",
		AgentID:     fixtures.DefaultAgentID,
		Service:     "github",
		Operation:   "credential.read",
	}); err == nil {
		t.Fatal("账本写不进去却报告成功")
	}
}

// approvalManagerOf 复用这条链路自己的审批 Manager 装配方式。
func approvalManagerOf(t *testing.T, all harness) *approval.Manager {
	t.Helper()

	manager, err := approval.NewManager(approval.Options{
		Approvals: repo.NewApprovals(all.db),
		Clock:     all.clock,
		IDs:       ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	return manager
}

func TestSettleApproval_AllowOnce_TightensAWiderScopeDownToOneRequest(t *testing.T) {
	// 「仅允许这一次」必须把次数上限收紧到 1，而不是沿用收敛出来的上限。
	// 这里刻意让决策记录里的上限是 5：默认值恰好是 1，用默认值测不出收紧这件事。
	all := newHarness(t)
	item := approvalOn(t, all, wideScope(t, 5))

	result := settle(t, all, item.ID, approval.ActionAllowOnce)

	if result.Lease == nil {
		t.Fatal("放行之后没有签发 Lease")
	}
	if result.Lease.RequestLimit != 1 {
		t.Errorf("次数上限为 %d，「仅允许这一次」要求 1", result.Lease.RequestLimit)
	}
}

func TestSettleApproval_AllowUntilTaskEnd_RaisesTheLimitAboveASingleCall(t *testing.T) {
	// 原先这条断言的是「收敛出来的上限原样生效」。那个契约有个后果没人注意到：
	// 收敛出来的默认上限是 1，于是「允许到任务结束」与「仅允许这一次」签出的
	// 授权一模一样 —— 用户点哪个都只换来一次调用（R-49）。
	//
	// 现在审批显式定下次数，收紧与放大都算数（`scope.DefaultRequestLimit` 的
	// 注释本来就写着「需要更多次由审批显式放大」）。放大的只有次数这一维。
	all := newHarness(t)
	item := approvalOn(t, all, wideScope(t, 5))

	result := settle(t, all, item.ID, approval.ActionAllowUntilTaskEnd)

	if result.Lease == nil {
		t.Fatal("放行之后没有签发 Lease")
	}
	if result.Lease.RequestLimit != scope.TaskRequestLimit {
		t.Errorf("次数上限为 %d，期望 %d", result.Lease.RequestLimit, scope.TaskRequestLimit)
	}
	if !result.Lease.IsSessionBound {
		// 次数放大之后，会话绑定就是这条授权最硬的那道边界。
		t.Error("「允许到任务结束」签出的授权没有绑定会话")
	}
}

func TestSettleApproval_AllowOnceAndAllowUntilTaskEnd_DifferInEffect(t *testing.T) {
	// 两个选项摆在用户面前，就得真的不一样。这一条守的是 R-49 本身：
	// 只要它们签出的授权在次数上相等，那次修复就白做了。
	once := newHarness(t)
	onceResult := settle(t, once, pending(t, once).ID, approval.ActionAllowOnce)

	task := newHarness(t)
	taskResult := settle(t, task, pending(t, task).ID, approval.ActionAllowUntilTaskEnd)

	if onceResult.Lease == nil || taskResult.Lease == nil {
		t.Fatal("两条路里有一条没有签发授权")
	}
	if onceResult.Lease.RequestLimit >= taskResult.Lease.RequestLimit {
		t.Errorf("「仅允许这一次」给了 %d 次，「允许到任务结束」给了 %d 次 —— 两个选项没有区别",
			onceResult.Lease.RequestLimit, taskResult.Lease.RequestLimit)
	}
	if onceResult.Lease.IsSessionBound {
		t.Error("「仅允许这一次」不该绑定会话")
	}
	if !taskResult.Lease.IsSessionBound {
		t.Error("「允许到任务结束」必须绑定会话")
	}
}

func TestSettleApproval_CautiousHighRisk_NeedsTwoConfirmationsAndStrongAuth(t *testing.T) {
	// PRD §11.1：谨慎模式下高风险要确认两次，且要走强认证（REQ-APPROVAL-005）。
	// 次数与强认证都取自决策记录与模式，不由提交方自报。
	all := build(t, nil, decision.ModeCautious)
	item := pendingHighRisk(t, all)

	oneConfirmation := pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce,
		StrongAuthCompleted: true, Confirmations: 1,
	}
	if _, err := all.pipeline.SettleApproval(t.Context(), oneConfirmation); err == nil {
		t.Fatal("只确认了一次却放行成功")
	}
	assertNoLease(t, all)

	withoutStrongAuth := pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce,
		StrongAuthCompleted: false, Confirmations: 2,
	}
	if _, err := all.pipeline.SettleApproval(t.Context(), withoutStrongAuth); err == nil {
		t.Fatal("没有完成强认证却放行成功")
	}
	assertNoLease(t, all)

	complete := pipeline.SettleInput{
		ApprovalID: item.ID, Action: approval.ActionAllowOnce,
		StrongAuthCompleted: true, Confirmations: 2,
	}
	result, err := all.pipeline.SettleApproval(t.Context(), complete)
	if err != nil {
		t.Fatalf("两次确认加强认证仍然被拒：%v", err)
	}
	if result.Lease == nil {
		t.Error("凑齐条件之后没有签发 Lease")
	}
}

// pendingHighRisk 跑一次高风险写操作，让它停在等待人工确认。
func pendingHighRisk(t *testing.T, all harness) approval.Approval {
	t.Helper()

	in := inputs(t, writeCall())
	in.Facts = pipeline.OperationFacts{Destructive: true}

	result := handle(t, all, in)
	if result.Approval == nil {
		t.Fatalf("结论为 %s，这条用例需要一个等待人工确认的请求", result.Outcome.Verdict)
	}

	record, err := repo.NewDecisions(all.db).DecisionByID(t.Context(), result.Decision.ID)
	if err != nil {
		t.Fatalf("读取决策失败：%v", err)
	}
	if record.RiskLevel != risk.LevelHigh {
		t.Fatalf("风险等级为 %s，这条用例需要高风险", record.RiskLevel)
	}
	return *result.Approval
}

// wideScope 造一个次数上限为 limit 的合法十维范围。
func wideScope(t *testing.T, limit int) string {
	t.Helper()

	granted := scope.Scope{
		AgentID:      fixtures.DefaultAgentID,
		WorkspaceID:  fixtures.DefaultWorkspaceID,
		Service:      fixtures.DefaultServiceLabel,
		IdentityID:   fixtures.DefaultIdentityID,
		Account:      "work",
		Resource:     map[string]string{"repo": "Runcoor/opendelo"},
		ResourceKey:  "repo=Runcoor/opendelo",
		Operation:    "pull_request.create",
		NotBefore:    fixtures.Instant,
		ExpiresAt:    fixtures.Instant.Add(scope.DefaultDuration),
		RequestLimit: limit,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
	if err := granted.Validate(); err != nil {
		t.Fatalf("造出来的范围不合法：%v", err)
	}
	encoded, err := json.Marshal(granted)
	if err != nil {
		t.Fatalf("范围无法编码：%v", err)
	}
	return string(encoded)
}

// approvalOn 用给定范围写一条决策并为它开一个审批项。
func approvalOn(t *testing.T, all harness, resolved string) approval.Approval {
	t.Helper()

	record := fixtures.Decision(fixtures.WithDecisionResolvedScope(resolved))
	if _, err := repo.NewDecisions(all.db).CreateDecision(t.Context(), record); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	item, err := approvalManagerOf(t, all).Open(t.Context(), record.ID)
	if err != nil {
		t.Fatalf("创建审批项失败：%v", err)
	}
	return item
}

func TestClearTrustMemory_WhenTheLedgerCannotBeWritten_TheMemoryStays(t *testing.T) {
	// ADR-004 与 REQ-UI-007 AC3：清除是破坏性操作，先记账本再删行。
	// 一次不在账本上的破坏性操作，比一次没做成的破坏性操作糟得多。
	all := newHarness(t)
	// 记忆由真实的审批落地产生：手写一行会撞上它对审批项的外键。
	learned := settle(t, all, pending(t, all).ID, approval.ActionAutoAllowInProject).Memory
	if learned == nil {
		t.Fatal("这条用例需要一条学过的记忆")
	}
	blind := rebuildWithAudit(t, all, failingAudit{err: errLedgerDown})

	if err := blind.ClearTrustMemory(t.Context(), learned.ID, "op-1"); err == nil {
		t.Fatal("账本写不进去时清除竟然成功了")
	}

	still, err := repo.NewTrustMemories(all.db).MemoryByID(t.Context(), learned.ID)
	if err != nil {
		t.Fatalf("那条记忆被删掉了：%v", err)
	}
	if still.ID != learned.ID {
		t.Errorf("读回来的是 %s", still.ID)
	}
}

func TestClearTrustMemory_RecordsWhichMemoryWasCleared(t *testing.T) {
	// 删掉之后这些字段就再也读不回来了，账本上只会剩一个主键。
	all := newHarness(t)
	before := settle(t, all, pending(t, all).ID, approval.ActionAutoAllowInProject).Memory
	if before == nil {
		t.Fatal("这条用例需要一条学过的记忆")
	}

	if clearErr := all.pipeline.ClearTrustMemory(t.Context(), before.ID, "op-1"); clearErr != nil {
		t.Fatalf("清除失败：%v", clearErr)
	}

	events, err := all.events.Events(t.Context(), time.Time{}, 100)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	var recorded audit.Event
	for _, event := range events {
		if event.Type == audit.EventTrustCleared {
			recorded = event
		}
	}
	if recorded.Service != before.Service || recorded.IdentityID != before.IdentityID {
		t.Errorf("账本上记的是 %s/%s，被清掉的是 %s/%s",
			recorded.Service, recorded.IdentityID, before.Service, before.IdentityID)
	}
	if recorded.AgentID != before.AgentID {
		t.Errorf("账本上的 Agent 是 %s，期望 %s", recorded.AgentID, before.AgentID)
	}
}
