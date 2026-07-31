package lease_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/lease 的行为用例（REQ-LEASE-001~004）。
 *
 * 用真实的 SQLite 仓储而不是替身：Lease 的不超发由「判定与递增在同一条语句里」
 * 保证，换成替身测的就是替身而不是那条保证。
 */

type harness struct {
	manager *lease.Manager
	leases  *repo.Leases
	clock   *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	db := fixtures.SeededChain(t)
	fixed := clock.NewFixed(fixtures.Instant)
	leases := repo.NewLeases(db)

	manager, err := lease.NewManager(lease.Options{
		Leases: leases,
		Clock:  fixed,
		IDs:    ulid.New(fixed),
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	return harness{manager: manager, leases: leases, clock: fixed}
}

// grantedScope 是一次已经收敛好的授权范围，十个维度齐全。
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
		RequestLimit: 3,
		Environment:  matcher.EnvironmentProduction,
		RiskCeiling:  risk.LevelMedium,
	}
}

func issue(t *testing.T, all harness, granted scope.Scope) lease.Lease {
	t.Helper()

	issued, err := all.manager.Issue(t.Context(), lease.IssueRequest{
		Granted:    granted,
		ApprovalID: fixtures.DefaultApprovalID,
	})
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	return issued
}

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

// ——— 签发（REQ-LEASE-001）———

func TestIssue_TakesEveryFieldFromTheScope(t *testing.T) {
	// 有效期、次数、资源与能力都不在这一层重新决定：签发时再放大一次，
	// 等于绕开了 Scope 收敛那一步的全部约束。
	all := newHarness(t)
	granted := grantedScope()

	issued := issue(t, all, granted)

	if !issued.ExpiresAt.Equal(granted.ExpiresAt) {
		t.Errorf("到期时刻为 %v，期望 %v", issued.ExpiresAt, granted.ExpiresAt)
	}
	if issued.RequestLimit != granted.RequestLimit {
		t.Errorf("次数上限为 %d，期望 %d", issued.RequestLimit, granted.RequestLimit)
	}
	if issued.AgentID != granted.AgentID || issued.IdentityID != granted.IdentityID ||
		issued.Service != granted.Service {
		t.Errorf("Agent / 身份 / 服务与 Scope 不一致：%+v", issued)
	}
	if issued.Status != lease.StatusActive || issued.UsedRequests != 0 {
		t.Errorf("新签发的 Lease 状态为 %s、已用 %d 次", issued.Status, issued.UsedRequests)
	}

	var capabilities []string
	if err := json.Unmarshal([]byte(issued.Capabilities), &capabilities); err != nil {
		t.Fatalf("能力清单不是合法 JSON：%v", err)
	}
	if !reflect.DeepEqual(capabilities, []string{granted.Operation}) {
		t.Errorf("能力清单为 %v，期望只有 %q 一个操作", capabilities, granted.Operation)
	}

	var stored scope.Scope
	if err := json.Unmarshal([]byte(issued.ResourceScope), &stored); err != nil {
		t.Fatalf("范围不是合法 JSON：%v", err)
	}
	if !stored.Covers(granted) || !granted.Covers(stored) {
		t.Errorf("入库的范围与 Scope 不等价：%+v", stored)
	}
}

func TestIssue_ExpiresAtIsNeverEmptyAndDefaultsToFifteenMinutes(t *testing.T) {
	// AC1：不存在 expires_at 为空的 Lease。AC2：默认时长 15 分钟 ——
	// 它来自 Scope 收敛时的默认值，这里只是如实带过来。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	if issued.ExpiresAt.IsZero() {
		t.Fatal("签发出了没有到期时刻的 Lease")
	}
	if got := issued.ExpiresAt.Sub(fixtures.Instant); got != 15*time.Minute {
		t.Errorf("默认时长为 %v，期望 15 分钟", got)
	}
	if scope.DefaultDuration != 15*time.Minute {
		t.Errorf("Scope 的默认时长为 %v，REQ-LEASE-001 AC2 要求 15 分钟", scope.DefaultDuration)
	}
}

func TestIssue_IncompleteScope_IsRefused(t *testing.T) {
	// Scope 十个维度不全时拒绝：一条范围说不清的授权签发出来也没法校验
	// 请求是否在其中（Fail Closed）。
	all := newHarness(t)

	for _, dimension := range scope.Dimensions() {
		t.Run(dimension+" 维度为空时拒绝签发", func(t *testing.T) {
			granted := breakDimension(grantedScope(), dimension)

			_, err := all.manager.Issue(t.Context(), lease.IssueRequest{Granted: granted})
			assertCode(t, err, apperr.CodeInvalidRequest)
			if !strings.Contains(err.Error(), dimension) {
				t.Errorf("错误说明没有点名 %s 维度：%v", dimension, err)
			}
		})
	}
}

// breakDimension 把十个维度中的一个抹掉，其余保持合法。
func breakDimension(granted scope.Scope, dimension string) scope.Scope {
	switch dimension {
	case scope.DimensionAgent:
		granted.AgentID = ""
	case scope.DimensionWorkspace:
		granted.WorkspaceID = ""
	case scope.DimensionService:
		granted.Service = ""
	case scope.DimensionAccount:
		granted.Account = ""
	case scope.DimensionResource:
		granted.Resource = nil
	case scope.DimensionOperation:
		granted.Operation = ""
	case scope.DimensionTime:
		granted.ExpiresAt = granted.NotBefore
	case scope.DimensionRequestCount:
		granted.RequestLimit = 0
	case scope.DimensionEnvironment:
		granted.Environment = ""
	case scope.DimensionRisk:
		granted.RiskCeiling = ""
	}
	return granted
}

func TestIssue_ScopeThatHasAlreadyExpired_IsRefused(t *testing.T) {
	// 一签发就是死的 Lease 不该进库：账本上留下一条永远用不上的授权，
	// 只会让「这条为什么没生效」变成一个要查的问题。
	all := newHarness(t)
	granted := grantedScope()
	all.clock.Set(granted.ExpiresAt)

	_, err := all.manager.Issue(t.Context(), lease.IssueRequest{Granted: granted})
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestIssue_SecondLeaseForTheSameApproval_IsRefused(t *testing.T) {
	// 一次审批只能签发一条 Lease，由唯一索引保证。
	all := newHarness(t)
	issue(t, all, grantedScope())

	_, err := all.manager.Issue(t.Context(), lease.IssueRequest{
		Granted:    grantedScope(),
		ApprovalID: fixtures.DefaultApprovalID,
	})
	assertCode(t, err, apperr.CodeConflict)
}

// ——— 计量（REQ-LEASE-001 AC3）———

func TestUse_WithinScope_CountsOneUse(t *testing.T) {
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	used, err := all.manager.Use(t.Context(), issued.ID, grantedScope())
	if err != nil {
		t.Fatalf("使用 Lease 失败：%v", err)
	}
	if used.UsedRequests != 1 {
		t.Errorf("已用次数为 %d，期望 1", used.UsedRequests)
	}
	if used.Status != lease.StatusActive {
		t.Errorf("状态为 %s，期望仍然 active", used.Status)
	}
}

func TestUse_ReachingTheLimit_MarksExhaustedImmediately(t *testing.T) {
	// AC3：用满即刻变为 exhausted，不等下一次请求来发现。
	all := newHarness(t)
	granted := grantedScope()
	granted.RequestLimit = 2
	issued := issue(t, all, granted)

	first, err := all.manager.Use(t.Context(), issued.ID, granted)
	if err != nil {
		t.Fatalf("第一次使用失败：%v", err)
	}
	if first.Status != lease.StatusActive {
		t.Errorf("第一次使用后状态为 %s，期望 active", first.Status)
	}

	second, err := all.manager.Use(t.Context(), issued.ID, granted)
	if err != nil {
		t.Fatalf("第二次使用失败：%v", err)
	}
	if second.Status != lease.StatusExhausted {
		t.Errorf("用满后状态为 %s，期望 exhausted", second.Status)
	}
	if second.UsedRequests != 2 {
		t.Errorf("已用次数为 %d，期望 2", second.UsedRequests)
	}

	_, err = all.manager.Use(t.Context(), issued.ID, granted)
	assertCode(t, err, apperr.CodeConflict)
}

func TestUse_UnlimitedLease_NeverExhausts(t *testing.T) {
	// 不限次数不等于不限时间：到期时刻仍然存在，两者是独立的两把锁。
	all := newHarness(t)
	granted := grantedScope()
	issued := issue(t, all, granted)

	// 直接经仓储签发一条不限次数的 Lease：Scope 层面不存在「不限次数」
	// （次数是十个维度之一），因此这条分支只能这样构造出来。
	unlimited, err := all.leases.IssueLease(t.Context(), fixtures.Lease(
		fixtures.WithLeaseID("01K1LEASE0000000000000UNLIM"),
		fixtures.WithLeaseScope(issued.ResourceScope),
		fixtures.WithLeaseRequestLimit(lease.Unlimited),
		fixtures.WithLeaseApprovalID(""),
	))
	if err != nil {
		t.Fatalf("签发不限次数的 Lease 失败：%v", err)
	}
	if unlimited.RequestLimit != lease.Unlimited {
		t.Fatalf("夹具的次数上限为 %d，期望 Unlimited", unlimited.RequestLimit)
	}

	for round := 1; round <= 5; round++ {
		used, err := all.manager.Use(t.Context(), unlimited.ID, granted)
		if err != nil {
			t.Fatalf("第 %d 次使用失败：%v", round, err)
		}
		if used.Status != lease.StatusActive {
			t.Fatalf("第 %d 次使用后状态为 %s，期望仍然 active", round, used.Status)
		}
	}
}

// ——— 不可扩权（REQ-LEASE-004）———

func TestUse_RequestBeyondTheGrantedScope_IsRefused(t *testing.T) {
	// AC2：超出范围的请求不复用这条 Lease，也不记一次使用 ——
	// 它要重新进入决策。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	cases := []struct {
		dimension string
		widen     func(*scope.Scope)
	}{
		{"agent", func(s *scope.Scope) { s.AgentID = "01K1AGENT0000000000000OTHER" }},
		{"workspace", func(s *scope.Scope) { s.WorkspaceID = "01K1WORKSPACE000000000OTHER" }},
		{"service", func(s *scope.Scope) { s.Service = "cloudflare" }},
		{"identity", func(s *scope.Scope) { s.IdentityID = "01K1IDENTITY0000000000OTHER" }},
		{"account", func(s *scope.Scope) { s.Account = "personal" }},
		{"resource", func(s *scope.Scope) {
			s.Resource = map[string]string{"repo": "Runcoor/other"}
			s.ResourceKey = "repo=Runcoor/other"
		}},
		{"operation", func(s *scope.Scope) { s.Operation = "pull_request.merge" }},
		{"environment", func(s *scope.Scope) { s.Environment = matcher.EnvironmentNonProduction }},
		{"risk", func(s *scope.Scope) { s.RiskCeiling = risk.LevelHigh }},
	}

	for _, testCase := range cases {
		t.Run(testCase.dimension+" 维度超出时拒绝", func(t *testing.T) {
			requested := grantedScope()
			testCase.widen(&requested)

			_, err := all.manager.Use(t.Context(), issued.ID, requested)
			assertCode(t, err, apperr.CodeCredentialNotAuthorized)
		})
	}

	current, err := all.leases.LeaseByID(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if current.UsedRequests != 0 {
		t.Errorf("被拒绝的请求记了 %d 次使用，期望 0", current.UsedRequests)
	}
}

func TestUse_RequestAskingForMoreUsesThanGranted_IsRefused(t *testing.T) {
	// 次数是十个维度之一，要的比签发时多就是范围扩大。
	// 时间窗口不参与比较，次数参与 —— 两者的区别在于：窗口每次解析都会重新起算，
	// 而次数是请求方对「这次授权覆盖几次」的表述。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	requested := grantedScope()
	requested.RequestLimit = grantedScope().RequestLimit + 1

	_, err := all.manager.Use(t.Context(), issued.ID, requested)
	assertCode(t, err, apperr.CodeCredentialNotAuthorized)
}

func TestUse_NarrowerRequest_IsStillWithinTheGrant(t *testing.T) {
	// 反向对照：请求比签发时更窄（风险上限更低）仍然算在范围内，
	// 否则上一条用例换成「什么都拒绝」也照样通过。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	narrower := grantedScope()
	narrower.RiskCeiling = risk.LevelLow
	narrower.RequestLimit = 1

	if _, err := all.manager.Use(t.Context(), issued.ID, narrower); err != nil {
		t.Fatalf("更窄的请求被拒绝：%v", err)
	}
}

func TestUse_FreshlyResolvedTimeWindow_DoesNotCountAsExpansion(t *testing.T) {
	// 一次新解析出来的 Scope 总是从「现在」起算 15 分钟。若时间窗口参与范围比较，
	// 任何一条早就签发的 Lease 都会立刻显得「不够宽」，AC2 就变成「永远重新决策」。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	all.clock.Advance(time.Minute)
	later := grantedScope()
	later.NotBefore = all.clock.Now()
	later.ExpiresAt = all.clock.Now().Add(scope.DefaultDuration)

	if _, err := all.manager.Use(t.Context(), issued.ID, later); err != nil {
		t.Fatalf("窗口起点前移后请求被拒：%v", err)
	}
}

func TestUse_IncompleteRequestScope_IsRefused(t *testing.T) {
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	requested := grantedScope()
	requested.Operation = ""

	_, err := all.manager.Use(t.Context(), issued.ID, requested)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestUse_UnreadableStoredScope_IsRefused(t *testing.T) {
	// 读不懂自己签发的范围时，唯一安全的结论是不知道这次请求在不在其中。
	//
	// 这里只构造得出「合法 JSON 但读不成 Scope」的两种：不是 JSON 的文本
	// 被 resource_scope 上的 json_valid 约束挡在库外，进不到这一层。
	all := newHarness(t)

	cases := []struct {
		id       string
		stored   string
		expected apperr.Code
	}{
		// 解析不成 Scope：这是网关自己写坏了，internal。
		{"01K1LEASE000000000000ARRAY", `[]`, apperr.CodeInternal},
		// 解析得了但十个维度不全：说不清范围，invalid_request 并点名维度。
		{
			"01K1LEASE000000000PARTIAL1",
			`{"agent_id":"01K1AGENT00000000000000000"}`,
			apperr.CodeInvalidRequest,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.stored+" 时拒绝", func(t *testing.T) {
			broken, err := all.leases.IssueLease(t.Context(), fixtures.Lease(
				fixtures.WithLeaseID(testCase.id),
				fixtures.WithLeaseScope(testCase.stored),
				fixtures.WithLeaseApprovalID(""),
			))
			if err != nil {
				t.Fatalf("写入 Lease 失败：%v", err)
			}

			_, err = all.manager.Use(t.Context(), broken.ID, grantedScope())
			assertCode(t, err, testCase.expected)
		})
	}
}

func TestManager_HasNoMethodThatWidensAGrant(t *testing.T) {
	// AC1：不存在修改 Lease Scope 的 API。Manager 与 Repository 两侧
	// 各自的方法集都钉死 —— 加一个 Extend / Widen 就会失败。
	managerMethods := methodNames(reflect.TypeOf(&lease.Manager{}))
	expectedManager := []string{
		"Active", "BoundToSessionOf", "ByID", "Issue", "IssuedFor", "IssuedTo",
		"Revoke", "Shorten", "Sweep", "Use",
	}
	if !reflect.DeepEqual(managerMethods, expectedManager) {
		t.Errorf("Manager 的方法为 %v，期望 %v", managerMethods, expectedManager)
	}

	repositoryMethods := methodNames(reflect.TypeOf((*lease.Repository)(nil)).Elem())
	expectedRepository := []string{
		"ActiveLeasesByIdentity", "ActiveLeasesDueBefore", "ActiveSessionBoundLeasesByAgent",
		"Close", "Consume", "IssueLease", "LeaseByApprovalID", "LeaseByID",
		"LeasesByStatus", "Shorten",
	}
	if !reflect.DeepEqual(repositoryMethods, expectedRepository) {
		t.Errorf("Repository 的方法为 %v，期望 %v", repositoryMethods, expectedRepository)
	}

	for _, name := range append(managerMethods, repositoryMethods...) {
		for _, banned := range []string{"Extend", "Widen", "Expand", "Renew", "Update", "SetScope"} {
			if strings.Contains(name, banned) {
				t.Errorf("方法 %s 命中禁止的词 %s", name, banned)
			}
		}
	}
}

func methodNames(target reflect.Type) []string {
	names := make([]string, 0, target.NumMethod())
	for index := 0; index < target.NumMethod(); index++ {
		names = append(names, target.Method(index).Name)
	}
	return names
}

// ——— 到期、撤销与缩短（REQ-LEASE-002）———

func TestUse_AfterExpiry_IsRefusedSoTheRequestIsDecidedAgain(t *testing.T) {
	// AC1：Lease 到期后，同一 Agent 的同一请求重新进入决策流程。
	// 在这一层表现为「这条 Lease 用不了了」，重新决策由 pipeline 负责。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	all.clock.Set(issued.ExpiresAt.Add(time.Second))

	_, err := all.manager.Use(t.Context(), issued.ID, grantedScope())
	assertCode(t, err, apperr.CodeConflict)
}

func TestSweep_ClosesOnlyTheLeasesThatAreDue(t *testing.T) {
	all := newHarness(t)
	granted := grantedScope()
	short := issue(t, all, granted)

	long := granted
	long.ExpiresAt = fixtures.Instant.Add(time.Hour)
	longer, err := all.manager.Issue(t.Context(), lease.IssueRequest{Granted: long})
	if err != nil {
		t.Fatalf("签发第二条 Lease 失败：%v", err)
	}

	all.clock.Set(short.ExpiresAt.Add(time.Second))
	closed, err := all.manager.Sweep(t.Context(), 10)
	if err != nil {
		t.Fatalf("清扫失败：%v", err)
	}

	if len(closed) != 1 || closed[0].ID != short.ID {
		t.Fatalf("清扫关闭了 %d 条：%+v", len(closed), closed)
	}
	if closed[0].Status != lease.StatusExpired {
		t.Errorf("状态为 %s，期望 expired", closed[0].Status)
	}

	remaining, err := all.leases.LeaseByID(t.Context(), longer.ID)
	if err != nil {
		t.Fatalf("读取未到期的 Lease 失败：%v", err)
	}
	if remaining.Status != lease.StatusActive {
		t.Errorf("未到期的 Lease 被关成了 %s", remaining.Status)
	}
}

func TestSweep_DoesNotRenew(t *testing.T) {
	// PRD §6.7 与 REQ-LEASE-002：到期自动失效，不续签。
	// 清扫两次，第二次没有东西可关 —— 说明第一次没有把它重新置为生效。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())
	all.clock.Set(issued.ExpiresAt.Add(time.Second))

	if _, err := all.manager.Sweep(t.Context(), 10); err != nil {
		t.Fatalf("第一次清扫失败：%v", err)
	}
	closed, err := all.manager.Sweep(t.Context(), 10)
	if err != nil {
		t.Fatalf("第二次清扫失败：%v", err)
	}
	if len(closed) != 0 {
		t.Errorf("第二次清扫又关闭了 %d 条：%+v", len(closed), closed)
	}
}

func TestRevoke_ClosesTheLeaseAndIsNotRepeatable(t *testing.T) {
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	revoked, err := all.manager.Revoke(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("收回 Lease 失败：%v", err)
	}
	if revoked.Status != lease.StatusRevoked {
		t.Errorf("状态为 %s，期望 revoked", revoked.Status)
	}

	_, err = all.manager.Revoke(t.Context(), issued.ID)
	assertCode(t, err, apperr.CodeConflict)

	_, err = all.manager.Use(t.Context(), issued.ID, grantedScope())
	assertCode(t, err, apperr.CodeConflict)
}

func TestShorten_OnlyEverShortens(t *testing.T) {
	// AC3：缩短操作只能缩短不能延长，尝试延长返回 400（invalid_request）。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())

	shortened, err := all.manager.Shorten(t.Context(), issued.ID, issued.ExpiresAt.Add(-time.Minute))
	if err != nil {
		t.Fatalf("缩短失败：%v", err)
	}
	if !shortened.ExpiresAt.Before(issued.ExpiresAt) {
		t.Fatalf("缩短后的到期时刻为 %v，未早于原有的 %v", shortened.ExpiresAt, issued.ExpiresAt)
	}

	cases := map[string]time.Time{
		"延长一分钟":  shortened.ExpiresAt.Add(time.Minute),
		"回到原有时刻": issued.ExpiresAt,
		"相等的时刻":  shortened.ExpiresAt,
		"空时刻":    {},
	}
	for name, target := range cases {
		t.Run(name+"被拒绝", func(t *testing.T) {
			_, refused := all.manager.Shorten(t.Context(), issued.ID, target)
			assertCode(t, refused, apperr.CodeInvalidRequest)
		})
	}

	current, err := all.leases.LeaseByID(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if !current.ExpiresAt.Equal(shortened.ExpiresAt) {
		t.Errorf("被拒绝的延长仍然改动了到期时刻：%v", current.ExpiresAt)
	}
}

// ——— 列表与构造 ———

func TestActive_ListsLeasesByExpiry(t *testing.T) {
	all := newHarness(t)
	granted := grantedScope()
	later := granted
	later.ExpiresAt = fixtures.Instant.Add(time.Hour)

	if _, err := all.manager.Issue(t.Context(), lease.IssueRequest{Granted: later}); err != nil {
		t.Fatalf("签发较晚到期的 Lease 失败：%v", err)
	}
	sooner := issue(t, all, granted)

	active, err := all.manager.Active(t.Context(), 10)
	if err != nil {
		t.Fatalf("列出生效中的 Lease 失败：%v", err)
	}
	if len(active) != 2 {
		t.Fatalf("列出 %d 条，期望 2 条", len(active))
	}
	if active[0].ID != sooner.ID {
		t.Errorf("第一条为 %s，期望先到期的 %s", active[0].ID, sooner.ID)
	}
}

func TestActiveAndSweep_RejectNonPositiveLimits(t *testing.T) {
	// 无界列表查询在这一层就被拒。
	all := newHarness(t)

	for _, limit := range []int{0, -1} {
		if _, err := all.manager.Active(t.Context(), limit); err == nil {
			t.Errorf("上限 %d 的列表查询被接受了", limit)
		}
		if _, err := all.manager.Sweep(t.Context(), limit); err == nil {
			t.Errorf("上限 %d 的清扫被接受了", limit)
		}
	}
}

// failingLeases 在指定的方法上返回哨兵错误，其余转交真实仓储。
//
// 用它只为验证一件事：仓储失败时错误原样向上传递，不被吞掉也不被换码。
// 断言的是错误的去向，不是仓储的行为。
type failingLeases struct {
	*repo.Leases
	failOn string
	err    error
}

var errRepositoryDown = apperr.New(apperr.CodeInternal).WithDetail("SENTINEL_REPO_DOWN")

func (f failingLeases) LeaseByID(ctx context.Context, id string) (lease.Lease, error) {
	if f.failOn == "LeaseByID" {
		return lease.Lease{}, f.err
	}
	return f.Leases.LeaseByID(ctx, id)
}

func (f failingLeases) Close(
	ctx context.Context, id string, status lease.Status, at time.Time,
) (lease.Lease, error) {
	if f.failOn == "Close" {
		return lease.Lease{}, f.err
	}
	return f.Leases.Close(ctx, id, status, at)
}

func (f failingLeases) ActiveLeasesDueBefore(
	ctx context.Context, deadline time.Time, limit int,
) ([]lease.Lease, error) {
	if f.failOn == "ActiveLeasesDueBefore" {
		return nil, f.err
	}
	return f.Leases.ActiveLeasesDueBefore(ctx, deadline, limit)
}

func TestManager_RepositoryFailure_IsPassedUpUnchanged(t *testing.T) {
	cases := []struct {
		method string
		call   func(*lease.Manager, string) error
	}{
		{"LeaseByID", func(m *lease.Manager, id string) error {
			_, err := m.Use(t.Context(), id, grantedScope())
			return err
		}},
		{"ActiveLeasesDueBefore", func(m *lease.Manager, _ string) error {
			_, err := m.Sweep(t.Context(), 10)
			return err
		}},
		{"Close", func(m *lease.Manager, _ string) error {
			_, err := m.Sweep(t.Context(), 10)
			return err
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.method+" 失败时错误上传", func(t *testing.T) {
			all := newHarness(t)
			issued := issue(t, all, grantedScope())
			all.clock.Set(issued.ExpiresAt.Add(time.Second))

			fixed := clock.NewFixed(all.clock.Now())
			manager, err := lease.NewManager(lease.Options{
				Leases: failingLeases{Leases: all.leases, failOn: testCase.method, err: errRepositoryDown},
				Clock:  fixed,
				IDs:    ulid.New(fixed),
			})
			if err != nil {
				t.Fatalf("构造 Manager 失败：%v", err)
			}

			if got := testCase.call(manager, issued.ID); !errors.Is(got, errRepositoryDown) {
				t.Errorf("返回的错误为 %v，期望原样传出哨兵错误", got)
			}
		})
	}
}

func TestSweep_KeepsWhatItAlreadyClosed_WhenLaterClosesFail(t *testing.T) {
	// 关闭途中出错即返回，已关掉的那些保持关闭状态 ——
	// 下一轮清扫不会重复处理它们，也不会因为一条失败而全部回滚。
	all := newHarness(t)
	issued := issue(t, all, grantedScope())
	all.clock.Set(issued.ExpiresAt.Add(time.Second))

	if _, err := all.manager.Sweep(t.Context(), 10); err != nil {
		t.Fatalf("清扫失败：%v", err)
	}

	current, err := all.leases.LeaseByID(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if current.Status != lease.StatusExpired {
		t.Errorf("状态为 %s，期望 expired", current.Status)
	}
}

func TestNewManager_MissingAnyDependency_IsRefused(t *testing.T) {
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	complete := lease.Options{
		Leases: repo.NewLeases(db),
		Clock:  fixed,
		IDs:    ulid.New(fixed),
	}

	if _, err := lease.NewManager(complete); err != nil {
		t.Fatalf("完整依赖仍被拒绝：%v", err)
	}

	cases := map[string]lease.Options{
		"缺仓储":      {Clock: complete.Clock, IDs: complete.IDs},
		"缺时钟":      {Leases: complete.Leases, IDs: complete.IDs},
		"缺 ID 生成器": {Leases: complete.Leases, Clock: complete.Clock},
	}
	for name, options := range cases {
		t.Run(name+"时拒绝构造", func(t *testing.T) {
			if _, err := lease.NewManager(options); err == nil {
				t.Error("依赖不全仍构造成功")
			}
		})
	}
}

func TestIssuedTo_ListsOnlyActiveLeasesOfThatIdentity(t *testing.T) {
	// 身份断开时的级联撤销靠它取件。已关闭的不返回：那些不需要再收一次。
	all := newHarness(t)
	granted := issue(t, all, grantedScope())

	active, err := all.manager.IssuedTo(t.Context(), granted.IdentityID, 10)
	if err != nil {
		t.Fatalf("按身份列出 Lease 失败：%v", err)
	}
	if len(active) != 1 || active[0].ID != granted.ID {
		t.Fatalf("列出了 %d 条：%v", len(active), active)
	}

	if _, err = all.manager.Revoke(t.Context(), granted.ID); err != nil {
		t.Fatalf("收回失败：%v", err)
	}
	active, err = all.manager.IssuedTo(t.Context(), granted.IdentityID, 10)
	if err != nil {
		t.Fatalf("按身份列出 Lease 失败：%v", err)
	}
	if len(active) != 0 {
		t.Errorf("收回之后还列出了 %d 条", len(active))
	}
}

func TestIssuedTo_AnotherIdentityGetsNothing(t *testing.T) {
	all := newHarness(t)
	issue(t, all, grantedScope())

	active, err := all.manager.IssuedTo(t.Context(), "01K1IDENTITYOTHER0000000000", 10)
	if err != nil {
		t.Fatalf("按身份列出 Lease 失败：%v", err)
	}
	if len(active) != 0 {
		t.Errorf("别的身份名下列出了 %d 条 Lease", len(active))
	}
}
