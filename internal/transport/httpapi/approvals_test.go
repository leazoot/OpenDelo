package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 审批与 Lease 端点。
 *
 * 待审批项由**真实的决策链路**产出，不是手写一行进库：这些用例要证明
 * 端点接的是那条链路，而一条自己塞进去的记录证明不了这件事。
 */

// waiting 跑一次写操作，让它停在等待人工确认，返回那个审批项。
func waiting(t *testing.T, all api) approval.Approval {
	t.Helper()

	result, err := all.backend.Services.Pipeline.Handle(t.Context(), pipeline.Inputs{
		Request:     fixtures.Request(),
		Call:        writeCall(),
		Catalog:     testCatalog(t),
		Identities:  []matcher.Identity{fixtures.Identity()},
		AgentTrust:  agentauth.TrustKnown,
		DeviceTrust: agentauth.DeviceTrusted,
	})
	if err != nil {
		t.Fatalf("决策链路失败：%v", err)
	}
	if result.Outcome.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %s，这条用例需要一个等待人工确认的请求", result.Outcome.Verdict)
	}
	return *result.Approval
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

func testCatalog(t *testing.T) *intent.Catalog {
	t.Helper()

	const capabilities = `[{"tool":"github.pull_request.create",` +
		`"operation":"pull_request.create","method":"POST",` +
		`"path":"/repos/{owner}/{repo}/pulls","risk":"medium",` +
		`"idempotent":false,"reversible":true,"sensitive_data":false,` +
		`"resource_keys":["repo"]}]`

	built, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.Declaration(fixtures.WithDeclarationCapabilities(capabilities)),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}
	return built
}

// ——— GET /v1/approvals ———

func TestListApprovals_ReturnsTheArrivalWithItsRequestAndDecision(t *testing.T) {
	// Gate 页面要一次拿齐「哪个 Agent、什么操作、什么风险」（REQ-APPROVAL-001）。
	all := newAPI(t)
	item := waiting(t, all)

	response := all.call(t, http.MethodGet, "/v1/approvals", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		Items []httpapi.ApprovalView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("返回了 %d 条待审批，期望 1 条", len(envelope.Items))
	}

	view := envelope.Items[0]
	if view.ID != item.ID {
		t.Errorf("审批项 id 为 %q，期望 %q", view.ID, item.ID)
	}
	if view.Request == nil || view.Request.Service == "" {
		t.Error("待审批项没有带上请求内容，界面无从说明这次在批什么")
	}
	if view.Decision == nil || view.Decision.RiskLevel == "" {
		t.Error("待审批项没有带上决策，界面无从说明风险等级")
	}
	if len(view.Decision.RiskFactors) == 0 {
		t.Error("没有风险因子，「为什么是这个等级」答不出来")
	}
}

func TestListApprovals_MediumRiskOffersLearningActionsHighRiskDoesNot(t *testing.T) {
	// REQ-APPROVAL-002：可选操作由后端给出，界面照着渲染。
	all := newAPI(t)
	waiting(t, all)

	response := all.call(t, http.MethodGet, "/v1/approvals", "")
	var envelope struct {
		Items []httpapi.ApprovalView `json:"items"`
	}
	decodeInto(t, response, &envelope)

	offered := envelope.Items[0].AvailableActions
	for _, wanted := range []string{"deny", "allow_once", "allow_until_task_end"} {
		if !containsString(offered, wanted) {
			t.Errorf("中风险没有提供 %s，可选操作为 %v", wanted, offered)
		}
	}
	if !containsString(offered, "auto_allow_in_project") {
		t.Errorf("中风险应当提供「今后自动允许」，可选操作为 %v", offered)
	}
	if !containsString(offered, "always_ask") {
		t.Errorf("中风险应当提供「始终要求确认」，可选操作为 %v", offered)
	}
}

func TestListApprovals_AgentIsRefused(t *testing.T) {
	// 审批是人的事。把「谁在等你点头」告诉发起方等于给它一份可以逐条催的清单。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodGet, "/v1/approvals", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

// ——— 四个决策端点 ———

func TestAllowOnce_IssuesExactlyOneLeaseLimitedToOneRequest(t *testing.T) {
	// REQ-APPROVAL-002 AC2：「仅允许这一次」签发 request_limit = 1 的 Lease。
	all := newAPI(t)
	item := waiting(t, all)

	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var settled settlement
	decodeInto(t, response, &settled)
	if settled.Lease == nil {
		t.Fatal("放行之后没有签发 Lease")
	}
	if settled.Lease.RequestLimit == nil || *settled.Lease.RequestLimit != 1 {
		t.Errorf("次数上限为 %v，「仅允许这一次」要求 1", settled.Lease.RequestLimit)
	}
	if settled.Replayed {
		t.Error("首次提交被当成了重放")
	}
	assertLeaseCount(t, all, 1)
}

func TestAllowOnce_RepeatedThreeTimes_ReturnsTheFirstResultAndOneLease(t *testing.T) {
	// REQ-API-004 AC1：连续调用三次只产生一个 Lease，且每次返回首次的结果。
	all := newAPI(t)
	item := waiting(t, all)

	var first settlement
	for attempt := 1; attempt <= 3; attempt++ {
		response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
		if response.Code != http.StatusOK {
			t.Fatalf("第 %d 次提交状态码为 %d，正文为 %s",
				attempt, response.Code, response.Body.String())
		}

		var settled settlement
		decodeInto(t, response, &settled)
		if settled.Lease == nil {
			t.Fatalf("第 %d 次提交没有返回 Lease", attempt)
		}
		if attempt == 1 {
			first = settled
			continue
		}
		if settled.Lease.ID != first.Lease.ID {
			t.Errorf("第 %d 次拿到的 Lease 是 %s，首次是 %s",
				attempt, settled.Lease.ID, first.Lease.ID)
		}
		if !settled.Replayed {
			t.Errorf("第 %d 次提交没有被标记为重放", attempt)
		}
	}

	assertLeaseCount(t, all, 1)
	assertAuditEventCount(t, all, "decision.user_allowed", 1)
}

func TestDeny_AfterwardsAllowingReturns409AndIssuesNoLease(t *testing.T) {
	// REQ-API-004 AC2：对已 deny 的 approval 调用 allow-once 返回 409。
	all := newAPI(t)
	item := waiting(t, all)

	denied := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/deny", "")
	if denied.Code != http.StatusOK {
		t.Fatalf("拒绝失败：%d %s", denied.Code, denied.Body.String())
	}

	var settled settlement
	decodeInto(t, denied, &settled)
	if settled.Lease != nil {
		t.Error("拒绝之后仍然签发了 Lease")
	}

	conflict := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409", conflict.Code)
	}
	if code := decodeErrorCode(t, conflict); code != "conflict" {
		t.Errorf("错误码为 %q", code)
	}
	assertLeaseCount(t, all, 0)
}

func TestDeny_RepeatedIsIdempotentToo(t *testing.T) {
	all := newAPI(t)
	item := waiting(t, all)

	for attempt := 1; attempt <= 2; attempt++ {
		response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/deny", "")
		if response.Code != http.StatusOK {
			t.Fatalf("第 %d 次拒绝状态码为 %d", attempt, response.Code)
		}
	}
	assertAuditEventCount(t, all, "decision.denied", 2)
}

func TestAllowTask_BindsTheLeaseToTheAgentSession(t *testing.T) {
	// REQ-APPROVAL-002 AC3。
	all := newAPI(t)
	item := waiting(t, all)

	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-task", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var settled settlement
	decodeInto(t, response, &settled)
	if settled.Lease == nil || !settled.Lease.IsSessionBound {
		t.Error("「允许到任务结束」没有把 Lease 绑定到 Agent Session")
	}
}

func TestAllowProject_LearnsExactlyOneMemory(t *testing.T) {
	// REQ-APPROVAL-002 AC4：「今后在当前项目自动允许」生成一条 Trust Memory。
	all := newAPI(t)
	item := waiting(t, all)

	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-project", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var settled settlement
	decodeInto(t, response, &settled)
	if settled.TrustMemoryID == "" {
		t.Fatal("没有学到记忆")
	}

	// 重复提交不会学第二条。
	again := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-project", "")
	var replayed settlement
	decodeInto(t, again, &replayed)
	if replayed.TrustMemoryID != "" {
		t.Error("重放又学了一条记忆")
	}
	if !replayed.Replayed {
		t.Error("重复提交没有被标记为重放")
	}
}

func TestAlwaysAsk_LearnsAMemoryThatKeepsAsking(t *testing.T) {
	// REQ-APPROVAL-002 AC5：「始终要求确认」生成一条 approval_behavior = always_ask
	// 的 Trust Memory。这次请求本身照常放行 —— 记下的是**今后**怎么办。
	all := newAPI(t)
	item := waiting(t, all)

	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/always-ask", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var settled settlement
	decodeInto(t, response, &settled)
	if settled.TrustMemoryID == "" {
		t.Fatal("没有学到记忆")
	}

	// 学到的必须是「命中也仍然询问」那一种。只断言「学到了一条」是不够的：
	// 学成 auto_allow 也能让上面那一句通过，而那与用户的选择正好相反。
	memory, err := all.backend.Memories.MemoryByID(t.Context(), settled.TrustMemoryID)
	if err != nil {
		t.Fatalf("读回记忆失败：%v", err)
	}
	if memory.Behavior != trust.BehaviorAlwaysAsk {
		t.Errorf("记忆的行为为 %q，期望 always_ask", memory.Behavior)
	}
	if settled.Approval.Action != "always_ask" {
		t.Errorf("审批项记下的操作为 %q，期望 always_ask", settled.Approval.Action)
	}
}

func TestAlwaysAsk_RepeatedSubmission_ReturnsTheFirstResult(t *testing.T) {
	// REQ-API-004：决策端点幂等，重复调用不产生第二条记忆。
	all := newAPI(t)
	item := waiting(t, all)

	first := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/always-ask", "")
	if first.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", first.Code, first.Body.String())
	}
	var settled settlement
	decodeInto(t, first, &settled)

	again := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/always-ask", "")
	if again.Code != http.StatusOK {
		t.Fatalf("重放的状态码为 %d，正文为 %s", again.Code, again.Body.String())
	}
	var replayed settlement
	decodeInto(t, again, &replayed)
	if !replayed.Replayed {
		t.Error("重复提交没有被标记为重放")
	}
	if replayed.TrustMemoryID != "" {
		t.Error("重放又学了一条记忆")
	}
	if replayed.Approval.ID != settled.Approval.ID {
		t.Errorf("重放返回的审批项为 %q，期望首次那一个 %q",
			replayed.Approval.ID, settled.Approval.ID)
	}
}

func TestAlwaysAsk_FromAnAgent_IsRefused(t *testing.T) {
	// 第五个端点与前四个受同一条边界约束：发起方不能决定自己的请求。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodPost, "/v1/approvals/anything/always-ask", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestAlwaysAsk_HighRisk_IsRefusedBecauseNoMemoryCanBeLearned(t *testing.T) {
	// 高风险审批不产生任何记忆（REQ-TRUST-003 AC1 的实现），因此这个操作在
	// 高风险下不在 available_actions 里，端点也照同一个答案拒绝 ——
	// 接入面不自己判断风险等级，可选操作只有 core/approval.Actions 一个来源。
	all := newAPI(t)
	item := waitingHighRisk(t, all)

	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/always-ask", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403，正文为 %s", response.Code, response.Body.String())
	}
}

// secondRound 把同一次请求再走一遍决策链路，用来看上一轮学到的记忆起了什么作用。
//
// 换一个请求主键：一个请求只有一个决策（唯一索引），沿用同一个会撞上它。
// 其余输入逐字相同 —— 两轮之间唯一变过的东西就是那条记忆。
func secondRound(t *testing.T, all api) decision.Verdict {
	t.Helper()

	again := fixtures.Request(fixtures.WithRequestID("01K1REQUEST000000000000777"))
	if _, err := all.backend.Requests.CreateRequest(t.Context(), again); err != nil {
		t.Fatalf("写入第二次请求失败：%v", err)
	}

	result, err := all.backend.Services.Pipeline.Handle(t.Context(), pipeline.Inputs{
		Request:     again,
		Call:        writeCall(),
		Catalog:     testCatalog(t),
		Identities:  []matcher.Identity{fixtures.Identity()},
		AgentTrust:  agentauth.TrustKnown,
		DeviceTrust: agentauth.DeviceTrusted,
	})
	if err != nil {
		t.Fatalf("第二轮决策失败：%v", err)
	}
	return result.Outcome.Verdict
}

func TestAlwaysAsk_TheMemoryItLearns_KeepsTheNextRequestAsking(t *testing.T) {
	// 这条用例是这个端点存在的理由：学到的那条记忆必须真的让下一次仍然停下来。
	// 与「今后自动允许」成对断言 —— 单独看「仍然要审批」是分不出对错的，
	// 因为没有任何记忆时同一次请求本来也要审批。
	cases := map[string]struct {
		path    string
		verdict decision.Verdict
	}{
		"今后自动允许之后不再问": {"allow-project", decision.VerdictAutoAllow},
		"始终要求确认之后仍然问": {"always-ask", decision.VerdictRequireApproval},
	}
	for name, each := range cases {
		t.Run(name, func(t *testing.T) {
			all := newAPI(t)
			item := waiting(t, all)

			response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/"+each.path, "")
			if response.Code != http.StatusOK {
				t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
			}
			var settled settlement
			decodeInto(t, response, &settled)
			if settled.TrustMemoryID == "" {
				t.Fatal("没有学到记忆，第二轮什么也证明不了")
			}

			if verdict := secondRound(t, all); verdict != each.verdict {
				t.Errorf("第二轮结论为 %s，期望 %s", verdict, each.verdict)
			}
		})
	}
}

func TestSettle_AgentIsRefused(t *testing.T) {
	// REQ-DECIDE-004 的边界：发起方不能决定自己的请求。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodPost, "/v1/approvals/anything/allow-once", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

func TestSettle_UnknownApprovalIsNotFound(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost, "/v1/approvals/01J0000000000000000NOPE/deny", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}

// ——— 辅助 ———

// settlement 是审批决策端点响应的解码形状。
type settlement struct {
	Approval httpapi.ApprovalView           `json:"approval"`
	Request  *httpapi.CapabilityRequestView `json:"request"`
	Lease    *httpapi.LeaseView             `json:"lease"`

	TrustMemoryID string `json:"trust_memory_id"`
	Replayed      bool   `json:"replayed"`
}

func containsString(all []string, wanted string) bool {
	for _, candidate := range all {
		if candidate == wanted {
			return true
		}
	}
	return false
}

func assertLeaseCount(t *testing.T, all api, expected int) {
	t.Helper()

	issued, err := all.backend.Leases.LeasesByStatus(t.Context(), lease.StatusActive, 100)
	if err != nil {
		t.Fatalf("列出 Lease 失败：%v", err)
	}
	if len(issued) != expected {
		t.Errorf("库里有 %d 条生效中的 Lease，期望 %d 条", len(issued), expected)
	}
}

func assertAuditEventCount(t *testing.T, all api, wanted string, expected int) {
	t.Helper()

	events, err := all.backend.Events.Events(t.Context(), zeroTime, 200)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}

	found := 0
	for _, event := range events {
		if string(event.Type) == wanted {
			found++
		}
	}
	if found != expected {
		t.Errorf("账本里有 %d 条 %s 事件，期望 %d 条", found, wanted, expected)
	}
}

// ——— 强认证不向 Agent 暴露（REQ-APPROVAL-005 AC3）———

func TestShowRequest_HidesStrongAuthFromTheAgentButNotFromTheConsole(t *testing.T) {
	// Agent 只需要知道自己在等人。知道人还要过一次 Passkey，就多了一条
	// 「什么时候有人正站在设备前」的线索。
	all := newBackend(t)

	request := fixtures.Request(
		fixtures.WithRequestID("01K1REQUEST00000000000STA1"),
		fixtures.WithRequestStatus(pipeline.StatusAwaitingApproval),
	)
	if _, err := all.Requests.CreateRequest(t.Context(), request); err != nil {
		t.Fatalf("写入请求失败：%v", err)
	}
	record := fixtures.Decision(
		fixtures.WithDecisionID("01K1DECISION0000000000STA1"),
		fixtures.WithDecisionRequestID(request.ID),
		fixtures.WithDecisionVerdict(decision.VerdictRequireApproval),
		fixtures.WithDecisionRiskLevel(risk.LevelHigh),
		fixtures.WithDecisionApprovalRequirement(decision.ApprovalStrongAuth),
	)
	if _, err := all.Decisions.CreateDecision(t.Context(), record); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}

	target := "/v1/capability-requests/" + request.ID

	console := newAPIWith(t, all, httpapi.Caller{}).call(t, http.MethodGet, target, "")
	var seenByConsole httpapi.CapabilityRequestView
	decodeInto(t, console, &seenByConsole)
	if seenByConsole.Decision == nil {
		t.Fatal("Console 拿不到决策记录")
	}
	// 反向对照：不断言 Console 看得见，这条用例在「两边都空」时也会通过。
	if seenByConsole.Decision.ApprovalRequirement != string(decision.ApprovalStrongAuth) {
		t.Fatalf("Console 看到的确认强度为 %q，期望 strong_auth",
			seenByConsole.Decision.ApprovalRequirement)
	}

	agent := newAPIWith(t, all, httpapi.Caller{AgentID: fixtures.DefaultAgentID}).
		call(t, http.MethodGet, target, "")
	var seenByAgent httpapi.CapabilityRequestView
	decodeInto(t, agent, &seenByAgent)
	if seenByAgent.Decision == nil {
		t.Fatal("Agent 拿不到自己请求的决策记录")
	}
	if seenByAgent.Decision.ApprovalRequirement != "" {
		t.Errorf("Agent 看到了确认强度 %q，它不该知道这一项",
			seenByAgent.Decision.ApprovalRequirement)
	}
	if seenByAgent.Decision.Verdict == "" {
		t.Error("Agent 连结论都拿不到了 —— 隐去的应该只是确认强度")
	}
}

// ——— 强认证（REQ-APPROVAL-005，用户决定 D-14 方案 C） ———

// highRiskCatalog 是一次高风险操作的能力映射表：删除仓库不可逆。
func highRiskCatalog(t *testing.T) *intent.Catalog {
	t.Helper()

	const capabilities = `[{"tool":"github.repository.delete",` +
		`"operation":"repository.delete","method":"DELETE",` +
		`"path":"/repos/{owner}/{repo}","risk":"high",` +
		`"idempotent":false,"reversible":false,"sensitive_data":false,` +
		`"resource_keys":["repo"]}]`

	built, err := intent.NewCatalog([]adapters.Declaration{
		fixtures.Declaration(fixtures.WithDeclarationCapabilities(capabilities)),
	})
	if err != nil {
		t.Fatalf("构造能力映射表失败：%v", err)
	}
	return built
}

// waitingHighRisk 跑一次高风险操作，让它停在「等人且要强认证」。
func waitingHighRisk(t *testing.T, all api) approval.Approval {
	t.Helper()

	result, err := all.backend.Services.Pipeline.Handle(t.Context(), pipeline.Inputs{
		Request: fixtures.Request(),
		Call: intent.Call{
			Tool:                "github.repository.delete",
			Resource:            `{"repo":"Runcoor/opendelo"}`,
			IdentityEnvironment: matcher.EnvironmentProduction,
		},
		Catalog:     highRiskCatalog(t),
		Identities:  []matcher.Identity{fixtures.Identity()},
		AgentTrust:  agentauth.TrustKnown,
		DeviceTrust: agentauth.DeviceTrusted,
	})
	if err != nil {
		t.Fatalf("决策链路失败：%v", err)
	}
	if result.Outcome.ApprovalRequirement != decision.ApprovalStrongAuth {
		t.Fatalf("确认强度为 %s，这条用例需要一个要强认证的请求",
			result.Outcome.ApprovalRequirement)
	}
	return *result.Approval
}

func TestSettle_HighRisk_IsRefusedWhileTheVaultIsLockedAndAllowedAfterUnlocking(t *testing.T) {
	// 强认证是服务端的事实：同一条审批，锁着时放不出去，
	// 主密码在 Gateway 上校验过之后才放得出去（REQ-APPROVAL-005 AC1）。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})
	item := waitingHighRisk(t, all)

	if locked := all.call(t, http.MethodPost, "/v1/vault/lock", ""); locked.Code != http.StatusOK {
		t.Fatalf("锁定失败：%d", locked.Code)
	}
	refused := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("锁着时状态码为 %d，期望 401，正文为 %s", refused.Code, refused.Body.String())
	}

	unlocked := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if unlocked.Code != http.StatusOK {
		t.Fatalf("解锁失败：%d %s", unlocked.Code, unlocked.Body.String())
	}
	allowed := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
	if allowed.Code != http.StatusOK {
		t.Fatalf("解锁后状态码为 %d，期望 200，正文为 %s", allowed.Code, allowed.Body.String())
	}
}

func TestSettle_WithoutAVault_HighRiskCanOnlyBeDenied(t *testing.T) {
	// 没装配保险库时认不出「有没有人在场」，就当成不在场（Fail Closed）。
	// 拒绝不受影响 —— 拿不到强认证不该连「不许」都说不出口。
	all := newAPI(t)
	item := waitingHighRisk(t, all)

	refused := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-task", "")
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("放行的状态码为 %d，期望 401，正文为 %s", refused.Code, refused.Body.String())
	}

	denied := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/deny", "")
	if denied.Code != http.StatusOK {
		t.Fatalf("拒绝的状态码为 %d，期望 200，正文为 %s", denied.Code, denied.Body.String())
	}
}

func TestSettle_ClaimingStrongAuthInTheBody_IsRefusedInsteadOfIgnored(t *testing.T) {
	// 调用方自称通过了强认证 —— 那一位不可提交。忽略它继续，会让调用方
	// 以为自己声明的认证生效了；直接 400 才说得清（同 submitRequest 的越权字段）。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})
	item := waitingHighRisk(t, all)

	if locked := all.call(t, http.MethodPost, "/v1/vault/lock", ""); locked.Code != http.StatusOK {
		t.Fatalf("锁定失败：%d", locked.Code)
	}
	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once",
		`{"strong_auth_completed":true,"confirmations":1}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400，正文为 %s", response.Code, response.Body.String())
	}

	settled, err := all.backend.Approvals.ApprovalByID(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("读取审批项失败：%v", err)
	}
	if settled.Status != approval.StatusPending {
		t.Errorf("审批项的状态变成了 %s，被拒的请求不该产生任何副作用", settled.Status)
	}
}

func TestUnlockVault_ThreeFailures_RecordTheLockoutInTheLedger(t *testing.T) {
	// REQ-APPROVAL-005 AC2：失败三次锁定 60 秒并记审计。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	for attempt := 1; attempt <= 3; attempt++ {
		response := all.call(t, http.MethodPost, "/v1/vault/unlock",
			`{"master_password":"`+wrongPassword+`"}`)
		want := http.StatusUnauthorized
		if attempt == 3 {
			want = http.StatusGatewayTimeout
		}
		if response.Code != want {
			t.Fatalf("第 %d 次的状态码为 %d，期望 %d", attempt, response.Code, want)
		}
	}

	events, err := all.backend.Events.Events(t.Context(), zeroTime, 200)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	locked := 0
	for _, event := range events {
		if event.Type != audit.EventStrongAuthLocked {
			continue
		}
		locked++
		if strings.Contains(event.Metadata, fixtures.VaultMasterPassword) ||
			strings.Contains(event.Metadata, wrongPassword) {
			t.Error("账本记下了主密码")
		}
	}
	if locked != 1 {
		t.Fatalf("账本里有 %d 条锁定记录，期望 1 条", locked)
	}
}

func TestUnlockVault_WhenTheLedgerCannotBeWritten_TheAttemptFails(t *testing.T) {
	// ADR-004：审计写入是前置条件。锁定这件事记不下来时，不能给出一条
	// 「已锁定」的正常答复 —— 那会是一次不在账本上的安全事件。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	for attempt := 1; attempt <= 2; attempt++ {
		if response := all.call(t, http.MethodPost, "/v1/vault/unlock",
			`{"master_password":"`+wrongPassword+`"}`); response.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次的状态码为 %d，期望 401", attempt, response.Code)
		}
	}
	if err := all.backend.DB.Close(); err != nil {
		t.Fatalf("关闭数据库失败：%v", err)
	}

	response := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+wrongPassword+`"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("状态码为 %d，期望 500，正文为 %s", response.Code, response.Body.String())
	}
}
