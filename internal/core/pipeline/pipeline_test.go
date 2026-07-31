package pipeline_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/pipeline 的集成用例。
 *
 * 全链路用真实实现跑：意图、匹配、收敛、风险、决策都是本尊，仓储是临时 SQLite。
 * 测试规则明确禁止在 core 包之间互相 mock ——
 * 那样测的是替身之间的接线，而不是这条链路。
 */

type harness struct {
	pipeline   *pipeline.Pipeline
	db         *store.DB
	events     *repo.AuditEvents
	leases     *repo.Leases
	requests   *repo.CapabilityRequests
	identities *repo.Identities
	clock      *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	return build(t, nil, decision.ModeBalanced)
}

// build 把整条链路装配起来。auditRepo 为 nil 时用真实的审计仓储。
func build(t *testing.T, auditRepo audit.Repository, mode decision.Mode) harness {
	t.Helper()

	return assemble(t, fixtures.SeededRequestChain(t), auditRepo, mode)
}

// rebuildWithAudit 在**同一个数据库**上换一套账本重新装配。
//
// 前半段用真实账本产出待审批项，后半段再让账本写不进去 ——
// 这是「审计写在放行之前」唯一能被单独测到的方式。
func rebuildWithAudit(t *testing.T, all harness, auditRepo audit.Repository) *pipeline.Pipeline {
	t.Helper()

	return assemble(t, all.db, auditRepo, decision.ModeBalanced).pipeline
}

// assemble 在给定数据库上装配一条链路。auditRepo 为 nil 时用真实的审计仓储。
func assemble(
	t *testing.T, db *store.DB, auditRepo audit.Repository, mode decision.Mode,
) harness {
	t.Helper()

	fixed := clock.NewFixed(fixtures.Instant)
	ids := ulid.New(fixed)

	events := repo.NewAuditEvents(db)
	if auditRepo == nil {
		auditRepo = events
	}
	recorder, err := audit.NewRecorder(auditRepo, fixed, ids)
	if err != nil {
		t.Fatalf("构造审计写入器失败：%v", err)
	}

	approvals, err := approval.NewManager(approval.Options{
		Approvals: repo.NewApprovals(db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	leases := repo.NewLeases(db)
	leaseManager, err := lease.NewManager(lease.Options{Leases: leases, Clock: fixed, IDs: ids})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}
	intents, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造意图解析器失败：%v", err)
	}
	scopes, err := scope.NewResolver(fixed)
	if err != nil {
		t.Fatalf("构造 Scope 收敛器失败：%v", err)
	}

	sessions, err := agentauth.NewService(agentauth.Options{
		Agents: repo.NewAgents(db), Devices: repo.NewDevices(db),
		Workspaces: repo.NewWorkspaces(db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Agent 会话服务失败：%v", err)
	}

	requests := repo.NewCapabilityRequests(db)
	identities := repo.NewIdentities(db)
	line, err := pipeline.New(pipeline.Options{
		Requests: requests, Decisions: repo.NewDecisions(db), Identities: identities,
		Agents:    sessions,
		Approvals: approvals, Leases: leaseManager, Memories: memories,
		Intents: intents, Scopes: scopes, Audit: recorder,
		Clock: fixed, IDs: ids, Mode: mode,
	})
	if err != nil {
		t.Fatalf("构造 Pipeline 失败：%v", err)
	}

	return harness{
		pipeline: line, db: db, events: events,
		leases: leases, requests: requests, identities: identities, clock: fixed,
	}
}

// readCall 是一次低风险读取：平衡模式下会被自动放行。
func readCall() intent.Call {
	return intent.Call{
		Tool:                "github.pull_request.list",
		Resource:            `{"repo":"Runcoor/opendelo"}`,
		IdentityEnvironment: matcher.EnvironmentProduction,
	}
}

// writeCall 是一次中风险写操作：没学过时要人确认。
func writeCall() intent.Call {
	return intent.Call{
		Tool:                "github.pull_request.create",
		Resource:            `{"repo":"Runcoor/opendelo"}`,
		DesiredChange:       `{"title":"修一个空指针"}`,
		IdentityEnvironment: matcher.EnvironmentProduction,
	}
}

// catalog 声明两个能力：一个读、一个写。
func catalog(t *testing.T) *intent.Catalog {
	t.Helper()

	const capabilities = `[{"tool":"github.pull_request.create",` +
		`"operation":"pull_request.create","method":"POST",` +
		`"path":"/repos/{owner}/{repo}/pulls","risk":"medium",` +
		`"idempotent":false,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["repo"]},` +
		`{"tool":"github.pull_request.list",` +
		`"operation":"pull_request.list","method":"GET",` +
		`"path":"/repos/{owner}/{repo}/pulls","risk":"low",` +
		`"idempotent":true,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["repo"]}]`

	built, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.Declaration(fixtures.WithDeclarationCapabilities(capabilities)),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}
	return built
}

func inputs(t *testing.T, call intent.Call) pipeline.Inputs {
	t.Helper()

	return pipeline.Inputs{
		Request:     fixtures.Request(),
		Call:        call,
		Catalog:     catalog(t),
		Identities:  []matcher.Identity{fixtures.Identity()},
		AgentTrust:  agentauth.TrustKnown,
		DeviceTrust: agentauth.DeviceTrusted,
	}
}

func handle(t *testing.T, all harness, in pipeline.Inputs) pipeline.Result {
	t.Helper()

	result, err := all.pipeline.Handle(t.Context(), in)
	if err != nil {
		t.Fatalf("链路失败：%v", err)
	}
	return result
}

func eventTypes(t *testing.T, all harness) []audit.EventType {
	t.Helper()

	events, err := all.events.Events(t.Context(), time.Time{}, 100)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	types := make([]audit.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

// ——— 三条主路径 ———

func TestHandle_LowRiskRead_IsAutoAllowedAndIssuesExactlyOneLease(t *testing.T) {
	all := newHarness(t)

	result := handle(t, all, inputs(t, readCall()))

	if result.Outcome.Verdict != decision.VerdictAutoAllow {
		t.Fatalf("结论为 %s，期望 auto_allow（原因 %s / %s）",
			result.Outcome.Verdict, result.Outcome.Reason, result.Outcome.Blocker)
	}
	if result.Lease == nil {
		t.Fatal("自动放行没有签发 Lease")
	}
	if result.Approval != nil {
		t.Error("自动放行还产生了审批项")
	}
	if result.Request.Status != pipeline.StatusAutoAllowed {
		t.Errorf("请求状态为 %s，期望 auto_allowed", result.Request.Status)
	}
	if result.Decision.Verdict != decision.VerdictAutoAllow {
		t.Errorf("决策记录里的结论为 %s", result.Decision.Verdict)
	}

	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != 1 {
		t.Fatalf("签发了 %d 条 Lease，期望 1 条", len(issued))
	}
	if !issued[0].ExpiresAt.Equal(fixtures.Instant.Add(scope.DefaultDuration)) {
		t.Errorf("Lease 到期时刻为 %v，期望 15 分钟后", issued[0].ExpiresAt)
	}
}

func TestHandle_MediumRiskWriteWithoutHistory_WaitsForAPerson(t *testing.T) {
	all := newHarness(t)

	result := handle(t, all, inputs(t, writeCall()))

	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，期望 require_approval", result.Outcome.Verdict)
	}
	if result.Approval == nil {
		t.Fatal("要人确认却没有产生审批项")
	}
	if result.Lease != nil {
		t.Error("还没有人确认就签发了 Lease")
	}
	if result.Request.Status != pipeline.StatusAwaitingApproval {
		t.Errorf("请求状态为 %s，期望 awaiting_approval", result.Request.Status)
	}
	if result.Approval.Status != approval.StatusPending {
		t.Errorf("审批项状态为 %s，期望 pending", result.Approval.Status)
	}
	// 一次普通的写操作算出来是中风险。资源数量默认当成 1 —— 少了这个默认值，
	// 每一次写都会因为「数量不确定」被当成批量修改而封顶到 high。
	if result.Decision.RiskLevel != risk.LevelMedium {
		t.Errorf("风险等级为 %s，期望 medium", result.Decision.RiskLevel)
	}

	issued, err := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != 0 {
		t.Errorf("等待审批期间签发了 %d 条 Lease", len(issued))
	}
}

func TestHandle_ForbiddenOperation_IsDeniedWithoutApprovalOrLease(t *testing.T) {
	all := newHarness(t)

	in := inputs(t, intent.Call{
		Tool:                "github.pull_request.list",
		Resource:            `{"repo":"Runcoor/opendelo"}`,
		IdentityEnvironment: matcher.EnvironmentProduction,
	})
	in.Blockers = []decision.Blocker{decision.BlockerGatewayOffline}

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Approval != nil || result.Lease != nil {
		t.Error("被拒绝的请求产生了审批项或 Lease")
	}
	if result.Request.Status != pipeline.StatusDenied {
		t.Errorf("请求状态为 %s，期望 denied", result.Request.Status)
	}
	if result.Outcome.Blocker != decision.BlockerGatewayOffline {
		t.Errorf("阻断为 %s，期望 gateway_offline", result.Outcome.Blocker)
	}
}

// ——— 审计是执行的前置条件（ADR-004）———

// failingAudit 让账本写入失败，其余不变。
type failingAudit struct{ err error }

var errLedgerDown = apperr.New(apperr.CodeInternal).WithDetail("SENTINEL_LEDGER_DOWN")

func (f failingAudit) Append(context.Context, audit.Event) (audit.Event, error) {
	return audit.Event{}, f.err
}

func (f failingAudit) EventByID(context.Context, string) (audit.Event, error) {
	return audit.Event{}, f.err
}

func (f failingAudit) Events(context.Context, time.Time, int) ([]audit.Event, error) {
	return nil, f.err
}

func (f failingAudit) EventsByAgent(
	context.Context, string, time.Time, int,
) ([]audit.Event, error) {
	return nil, f.err
}

func (f failingAudit) EventsByService(
	context.Context, string, time.Time, int,
) ([]audit.Event, error) {
	return nil, f.err
}

func (f failingAudit) CountBefore(context.Context, time.Time) (int, error) {
	return 0, f.err
}

func (f failingAudit) PruneBefore(
	context.Context, time.Time, audit.Event,
) (int, audit.Event, error) {
	return 0, audit.Event{}, f.err
}

func TestHandle_AuditWriteFailure_FailsTheWholeRequestAndIssuesNothing(t *testing.T) {
	// ADR-004：审计写入是执行的前置条件。一次没有留下记录的执行事后无从追溯，
	// 而 REQ-AUDIT-001 AC1 要求「无未审计路径」。
	for _, call := range []intent.Call{readCall(), writeCall()} {
		t.Run(call.Tool, func(t *testing.T) {
			all := build(t, failingAudit{err: errLedgerDown}, decision.ModeBalanced)

			_, err := all.pipeline.Handle(t.Context(), inputs(t, call))
			if !errors.Is(err, errLedgerDown) {
				t.Fatalf("返回的错误为 %v，期望账本写入失败原样上传", err)
			}

			// 用另一套仓储读同一个数据库：账本写不进去时，Lease 一条都不该有。
			issued, listErr := all.leases.LeasesByStatus(t.Context(), lease.StatusActive, 10)
			if listErr != nil {
				t.Fatalf("列出 Lease 失败：%v", listErr)
			}
			if len(issued) != 0 {
				t.Errorf("账本写不进去却签发了 %d 条 Lease", len(issued))
			}
		})
	}
}

func TestHandle_EveryPathLeavesAnAuditTrail(t *testing.T) {
	// REQ-AUDIT-001 AC1：无未审计路径。三条主路径各自留下记录。
	cases := []struct {
		name     string
		build    func(*testing.T) (harness, pipeline.Inputs)
		expected audit.EventType
	}{
		{"自动放行", func(t *testing.T) (harness, pipeline.Inputs) {
			return newHarness(t), inputs(t, readCall())
		}, audit.EventAutoAllowed},
		{"要人确认", func(t *testing.T) (harness, pipeline.Inputs) {
			return newHarness(t), inputs(t, writeCall())
		}, audit.EventDenied},
		{"Fail Closed", func(t *testing.T) (harness, pipeline.Inputs) {
			all := newHarness(t)
			in := inputs(t, readCall())
			in.Call.Tool = "github.repo.delete"
			return all, in
		}, audit.EventError},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			all, in := testCase.build(t)
			handle(t, all, in)

			types := eventTypes(t, all)
			if len(types) == 0 {
				t.Fatal("这条路径没有留下任何审计记录")
			}
			found := false
			for _, recorded := range types {
				if recorded == testCase.expected {
					found = true
				}
			}
			if !found {
				t.Errorf("记下的事件为 %v，期望其中有 %s", types, testCase.expected)
			}
		})
	}
}

// ——— Fail Closed 与 panic ———

func TestHandle_UndeclaredCapability_IsDenied(t *testing.T) {
	all := newHarness(t)
	in := inputs(t, readCall())
	in.Call.Tool = "github.repo.delete"

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Outcome.Blocker != decision.BlockerCapabilityNotOffered {
		t.Errorf("阻断为 %s，期望 capability_not_offered", result.Outcome.Blocker)
	}
	if result.Lease != nil {
		t.Error("未声明的能力签发了 Lease")
	}
}

func TestHandle_WithoutAnyIdentity_IsDenied(t *testing.T) {
	all := newHarness(t)
	in := inputs(t, readCall())
	in.Identities = nil

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Outcome.Blocker != decision.BlockerIdentityAmbiguityUnresolvable {
		t.Errorf("阻断为 %s，期望 identity_ambiguity_unresolvable", result.Outcome.Blocker)
	}
}

func TestHandle_WithoutAWorkspace_IsDeniedBecauseScopeCannotBeConverged(t *testing.T) {
	// Scope 的十个维度里少了项目那一维，收敛不出来 —— Fail Closed。
	all := newHarness(t)
	in := inputs(t, readCall())
	in.Request.WorkspaceID = ""

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Outcome.Blocker != decision.BlockerScopeUndeterminable {
		t.Errorf("阻断为 %s，期望 scope_undeterminable", result.Outcome.Blocker)
	}
}

func TestHandle_RequestWithoutResourceText_StillLeavesAnAuditTrail(t *testing.T) {
	// 「这次请求没有资源字段」本身也是一条要如实记下的事实，
	// 不能因为 resource 列有 json_valid 约束就写不进账本。
	all := newHarness(t)
	in := inputs(t, readCall())
	in.Request.Resource = ""
	in.Call.Tool = "github.repo.delete"

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if len(eventTypes(t, all)) == 0 {
		t.Error("这条路径没有留下任何审计记录")
	}
}

func TestHandle_AuditFailureOnTheFailClosedPath_AlsoFailsTheRequest(t *testing.T) {
	// 一次「因为认不出而被拒」的请求同样必须留下记录，
	// 否则「无未审计路径」（REQ-AUDIT-001 AC1）就不成立。
	all := build(t, failingAudit{err: errLedgerDown}, decision.ModeBalanced)
	in := inputs(t, readCall())
	in.Call.Tool = "github.repo.delete"

	if _, err := all.pipeline.Handle(t.Context(), in); !errors.Is(err, errLedgerDown) {
		t.Errorf("返回的错误为 %v，期望账本写入失败原样上传", err)
	}
}

func TestHandle_MemoryForAnotherResource_DoesNotCountAsHistory(t *testing.T) {
	// 学过 another-repo 不等于学过 opendelo：范围超出已学习的授权，必须重新问。
	//
	// 值得记一笔的是**它在哪一分支被拦下**：风险引擎先因为 beyond_history
	// 把中风险上调成高风险，于是第二分支就命中了，
	// 轮不到第三分支。两条路都通向「要人确认」，但账本上的原因不同。
	all := newHarness(t)

	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(all.db), Clock: all.clock, IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}
	elsewhere := grantedScope()
	elsewhere.Resource = map[string]string{"repo": "Runcoor/another"}
	elsewhere.ResourceKey = "repo=Runcoor/another"

	if _, err := repo.NewDecisions(all.db).CreateDecision(
		t.Context(), fixtures.Decision()); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	if _, err := repo.NewApprovals(all.db).CreateApproval(
		t.Context(), fixtures.Approval()); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
	if _, err := memories.Generate(t.Context(), trust.GenerateRequest{
		Approved:   elsewhere,
		Learned:    elsewhere,
		ApprovalID: fixtures.DefaultApprovalID,
		RiskLevel:  risk.LevelMedium,
		Behavior:   trust.BehaviorAutoAllow,
	}); err != nil {
		t.Fatalf("生成记忆失败：%v", err)
	}

	const secondID = "01K1REQUEST00000000000OTHER"
	if _, err := all.requests.CreateRequest(t.Context(), fixtures.Request(
		fixtures.WithRequestID(secondID),
	)); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	in := inputs(t, writeCall())
	in.Request.ID = secondID

	result := handle(t, all, in)
	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，期望 require_approval", result.Outcome.Verdict)
	}
	if result.Outcome.Reason != decision.ReasonHighRisk {
		t.Errorf("原因为 %s，期望 high_risk", result.Outcome.Reason)
	}
	if result.Decision.RiskLevel != risk.LevelHigh {
		t.Errorf("风险等级为 %s，期望被 beyond_history 上调到 high", result.Decision.RiskLevel)
	}
}

func TestHandle_UnknownAgentTrust_IsDeniedBecauseRiskCannotBeComputed(t *testing.T) {
	// 风险等级未知是 Fail Closed 的十种情况之一：算不出来就拒绝，
	// 不落到某个「看起来温和」的等级上。
	all := newHarness(t)
	in := inputs(t, readCall())
	in.AgentTrust = "root"

	result := handle(t, all, in)

	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Outcome.Blocker != decision.BlockerRiskUnknown {
		t.Errorf("阻断为 %s，期望 risk_unknown", result.Outcome.Blocker)
	}
	if result.Lease != nil {
		t.Error("风险算不出来却签发了 Lease")
	}
}

func TestHandle_PanicInsideTheChain_BecomesDeny(t *testing.T) {
	// REQ-DECIDE-002 AC2：链路内部 panic 被 recover 后结果为 deny + error 级审计。
	// 用一个会让 Catalog 解引用崩掉的输入制造它。
	all := newHarness(t)
	in := inputs(t, readCall())
	in.Catalog = nil

	result, err := all.pipeline.Handle(t.Context(), in)
	if err != nil {
		t.Fatalf("panic 之后返回了错误：%v", err)
	}
	if result.Outcome.Verdict != decision.VerdictDeny {
		t.Fatalf("结论为 %s，期望 deny", result.Outcome.Verdict)
	}
	if result.Lease != nil {
		t.Error("崩掉的链路签发了 Lease")
	}
}

func TestHandle_RequestWithoutAnOperationID_IsRefused(t *testing.T) {
	// 没有 operation_id 的请求写不出可追溯的审计记录，不能让它开跑。
	all := newHarness(t)

	for _, broken := range []func(*pipeline.Inputs){
		func(in *pipeline.Inputs) { in.Request.ID = "" },
		func(in *pipeline.Inputs) { in.Request.OperationID = "" },
	} {
		in := inputs(t, readCall())
		broken(&in)

		_, err := all.pipeline.Handle(t.Context(), in)
		if err == nil {
			t.Fatal("缺主键或 operation_id 的请求仍然开跑了")
		}
		// 说明必须来自本包：账本与外键也会各自拦下这样的请求，
		// 只看「有错」分不出是谁拒的，也分不出它有没有先跑了半条链路。
		if !strings.Contains(err.Error(), "能力请求缺少") {
			t.Errorf("拒绝理由为 %v，期望来自本包的前置校验", err)
		}
	}
}

// ——— 唯一放行出口（代码层面的审查）———

func TestPipeline_IssuesALeaseFromExactlyOnePlace(t *testing.T) {
	// 「放行出口唯一」是一条可以被机器审查的性质：整个包里
	// 只允许有一处调用 leases.Issue。多一处就多一条要单独证明的放行路径。
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("取当前目录失败：%v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("列出源码目录失败：%v", err)
	}

	sites := 0
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(root, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("解析 %s 失败：%v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Issue" {
				return true
			}
			receiver, ok := selector.X.(*ast.SelectorExpr)
			if ok && receiver.Sel.Name == "leases" {
				sites++
			}
			return true
		})
	}

	if scanned == 0 {
		t.Fatal("没有扫描到任何生产代码文件，检查等于没做")
	}
	if sites != 1 {
		t.Errorf("包里有 %d 处签发 Lease 的调用，期望恰好 1 处", sites)
	}
}

func TestNew_MissingAnyDependency_IsRefused(t *testing.T) {
	all := newHarness(t)
	fixed := clock.NewFixed(fixtures.Instant)
	ids := ulid.New(fixed)

	recorder, err := audit.NewRecorder(all.events, fixed, ids)
	if err != nil {
		t.Fatalf("构造审计写入器失败：%v", err)
	}
	intents, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造意图解析器失败：%v", err)
	}
	scopes, err := scope.NewResolver(fixed)
	if err != nil {
		t.Fatalf("构造 Scope 收敛器失败：%v", err)
	}
	approvals, err := approval.NewManager(approval.Options{
		Approvals: repo.NewApprovals(all.db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	leaseManager, err := lease.NewManager(lease.Options{
		Leases: all.leases, Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(all.db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}

	sessions, err := agentauth.NewService(agentauth.Options{
		Agents: repo.NewAgents(all.db), Devices: repo.NewDevices(all.db),
		Workspaces: repo.NewWorkspaces(all.db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Agent 会话服务失败：%v", err)
	}

	complete := pipeline.Options{
		Requests: all.requests, Decisions: repo.NewDecisions(all.db),
		Identities: all.identities, Agents: sessions,
		Approvals: approvals, Leases: leaseManager, Memories: memories,
		Intents: intents, Scopes: scopes, Audit: recorder, Clock: fixed, IDs: ids,
	}
	if _, err := pipeline.New(complete); err != nil {
		t.Fatalf("完整依赖仍被拒绝：%v", err)
	}

	blanks := []func(*pipeline.Options){
		func(o *pipeline.Options) { o.Requests = nil },
		func(o *pipeline.Options) { o.Decisions = nil },
		func(o *pipeline.Options) { o.Identities = nil },
		func(o *pipeline.Options) { o.Agents = nil },
		func(o *pipeline.Options) { o.Approvals = nil },
		func(o *pipeline.Options) { o.Leases = nil },
		func(o *pipeline.Options) { o.Memories = nil },
		func(o *pipeline.Options) { o.Intents = nil },
		func(o *pipeline.Options) { o.Scopes = nil },
		func(o *pipeline.Options) { o.Audit = nil },
		func(o *pipeline.Options) { o.Clock = nil },
		func(o *pipeline.Options) { o.IDs = nil },
	}
	for index, blank := range blanks {
		options := complete
		blank(&options)
		if _, err := pipeline.New(options); err == nil {
			t.Errorf("第 %d 项依赖缺失仍构造成功", index+1)
		}
	}
}

// ——— 学到的授权确实会被复用 ———

func TestHandle_AfterLearning_TheSameWriteIsAutoAllowed(t *testing.T) {
	// 把决策链路的六个包串起来看：第一次写要人确认，学一条记忆之后同样的写
	// 直接放行 —— 这正是决策第六分支（中风险且完全匹配 Trust Memory）。
	all := newHarness(t)

	first := handle(t, all, inputs(t, writeCall()))
	if first.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("首次写操作的结论为 %s，期望 require_approval", first.Outcome.Verdict)
	}

	// 用户选择「今后在当前项目自动允许」：审批放行并学一条记忆。
	approvals, err := approval.NewManager(approval.Options{
		Approvals: repo.NewApprovals(all.db), Clock: all.clock, IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	settlement, err := approvals.Settle(t.Context(), approval.SettleRequest{
		ApprovalID:            first.Approval.ID,
		Action:                approval.ActionAutoAllowInProject,
		RiskLevel:             first.Decision.RiskLevel,
		Requirement:           first.Outcome.ApprovalRequirement,
		RequiredConfirmations: 1,
		Confirmations:         1,
	})
	if err != nil {
		t.Fatalf("放行失败：%v", err)
	}

	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(all.db), Clock: all.clock, IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}
	granted := grantedScope()
	if _, err := memories.Generate(t.Context(), trust.GenerateRequest{
		Approved:   granted,
		Learned:    granted,
		ApprovalID: settlement.Approval.ID,
		RiskLevel:  risk.LevelMedium,
		Behavior:   settlement.Learn,
	}); err != nil {
		t.Fatalf("生成记忆失败：%v", err)
	}

	// 同样的写操作再来一次：这一次直接放行。
	const secondID = "01K1REQUEST0000000000SECOND"
	if _, err := all.requests.CreateRequest(t.Context(), fixtures.Request(
		fixtures.WithRequestID(secondID),
	)); err != nil {
		t.Fatalf("写入第二条能力请求失败：%v", err)
	}

	second := inputs(t, writeCall())
	second.Request.ID = secondID
	result := handle(t, all, second)

	if result.Outcome.Verdict != decision.VerdictAutoAllow {
		t.Fatalf("学过之后的结论为 %s（原因 %s），期望 auto_allow",
			result.Outcome.Verdict, result.Outcome.Reason)
	}
	if result.Outcome.Reason != decision.ReasonTrustMemoryMatch {
		t.Errorf("原因为 %s，期望 trust_memory_match", result.Outcome.Reason)
	}
	if result.Lease == nil {
		t.Fatal("命中记忆却没有签发 Lease")
	}
}

func grantedScope() scope.Scope {
	return scope.Scope{
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
		RequestLimit: 1,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
}
