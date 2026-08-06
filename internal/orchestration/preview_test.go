package orchestration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 查勘接进决策链路的用例（REQ-APPROVAL-001 AC4）。
 *
 * 这里跑的是真 Pipeline、真数据库、真 Adapter 注册表；被替掉的只有出站那一步 ——
 * 它在 adapter 那一侧已经有自己的用例（外部服务
 * 用 fake，core 之间不互相 mock）。替掉它是为了能证明**它有没有被调用**，
 * 而这恰恰是本任务四条限定里三条的形状。
 */

// recordingPreviews 记下查勘被调用了几次、拿到的是什么。
type recordingPreviews struct {
	calls    int
	last     adapters.ExchangeRequest
	changes  []adapters.ResourceChange
	failWith error
}

func (r *recordingPreviews) Preview(
	_ context.Context, request adapters.ExchangeRequest,
) (adapters.PreviewOutput, error) {
	r.calls++
	r.last = request
	if r.failWith != nil {
		return adapters.PreviewOutput{}, r.failWith
	}
	return adapters.PreviewOutput{Changes: r.changes}, nil
}

// declaredWrite 是一份两边都认得的写能力：编译期注册表提供 create_issue，
// 数据库声明把工具名映射到它。少了任何一边，请求会在意图解析处被拒，
// 那时查勘该不该发生这个问题根本轮不到。
const declaredWrite = `[{"tool":"github.issue.create","operation":"create_issue",` +
	`"method":"POST","path":"/repos/{owner}/{repo}/issues","risk":"medium",` +
	`"idempotent":false,"reversible":true,"sensitive_data":false,` +
	`"resource_keys":["owner","repo"]}]`

const declaredRead = `[{"tool":"github.repository.read","operation":"read_repository",` +
	`"method":"GET","path":"/repos/{owner}/{repo}","risk":"low","idempotent":true,` +
	`"reversible":true,"sensitive_data":false,"resource_keys":["owner","repo"]}]`

const declaredDelete = `[{"tool":"github.repository.delete","operation":"delete_repository",` +
	`"method":"DELETE","path":"/repos/{owner}/{repo}","risk":"high","idempotent":true,` +
	`"reversible":false,"sensitive_data":false,"resource_keys":["owner","repo"]}]`

type previewHarness struct {
	gateway  fixtures.Gateway
	decide   *orchestration.Submissions
	previews *recordingPreviews
}

func newPreviewHarness(t *testing.T, capabilities string, previews *recordingPreviews) previewHarness {
	t.Helper()

	return newPreviewHarnessWithArrivals(t, capabilities, previews, &recordingArrivals{})
}

// newPreviewHarnessWithArrivals 与 newPreviewHarness 相同，但让调用方拿到到达通知。
func newPreviewHarnessWithArrivals(
	t *testing.T, capabilities string, previews *recordingPreviews, arrivals *recordingArrivals,
) previewHarness {
	t.Helper()

	gateway := fixtures.NewGateway(t)
	if _, err := repo.NewServiceAdapters(gateway.DB).CreateDeclaration(t.Context(),
		fixtures.Declaration(fixtures.WithDeclarationCapabilities(capabilities))); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	adapter, err := github.New(github.Options{})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	registry, err := adapters.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	decide, err := orchestration.New(orchestration.Submissions{
		Pipeline:   gateway.Services.Pipeline,
		Identities: repo.NewIdentities(gateway.DB), Agents: repo.NewAgents(gateway.DB),
		Devices: repo.NewDevices(gateway.DB), Declarations: repo.NewServiceAdapters(gateway.DB),
		Registry: registry, Previews: previews, Requests: gateway.Requests,
		Arrivals: arrivals,
		Clock:    gateway.Clock, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造请求编排失败：%v", err)
	}
	return previewHarness{gateway: gateway, decide: decide, previews: previews}
}

// submit 落一条能力请求并跑完链路。
func (h previewHarness) submit(t *testing.T, operation, desiredChange string) pipeline.Result {
	t.Helper()

	created, err := h.gateway.Requests.CreateRequest(t.Context(), pipeline.CapabilityRequest{
		ID: "request_preview_" + operation, OperationID: "operation_preview_" + operation,
		AgentID: fixtures.DefaultAgentID, WorkspaceID: fixtures.DefaultWorkspaceID,
		Service: "github", Operation: operation,
		Resource: `{"owner":"Runcoor","repo":"opendelo"}`, DesiredChange: desiredChange,
		Reason: "用例", Status: pipeline.StatusReceived,
		CreatedAt: h.gateway.Clock.Now(), UpdatedAt: h.gateway.Clock.Now(),
	})
	if err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	result, err := h.decide.Decide(t.Context(), created)
	if err != nil {
		t.Fatalf("决策失败：%v", err)
	}
	return result
}

func (h previewHarness) storedPreview(t *testing.T, id string) string {
	t.Helper()

	stored, err := h.gateway.Requests.RequestByID(t.Context(), id)
	if err != nil {
		t.Fatalf("读回能力请求失败：%v", err)
	}
	return stored.ChangePreview
}

func TestDecide_AwaitingApproval_QueriesTheOldValueAndRecordsIt(t *testing.T) {
	previews := &recordingPreviews{changes: []adapters.ResourceChange{
		{Resource: "opendelo", Field: "title", Before: "旧标题", After: "修一个空指针"},
	}}
	harness := newPreviewHarness(t, declaredWrite, previews)

	result := harness.submit(t, "create_issue", `{"title":"修一个空指针"}`)
	if result.Decision.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %q，期望 require_approval —— 这条用例要的是停下来等人的那一支",
			result.Decision.Verdict)
	}

	if previews.calls != 1 {
		t.Fatalf("查勘被调用了 %d 次，期望恰好 1 次", previews.calls)
	}
	// 身份已匹配之后才查勘：拿不到身份就不知道该用谁的凭据（第二条限定）。
	if previews.last.IdentityID != result.Decision.IdentityID || previews.last.IdentityID == "" {
		t.Errorf("查勘用的身份为 %q，期望决策匹配到的 %q",
			previews.last.IdentityID, result.Decision.IdentityID)
	}
	// 打向的是**收敛后的 Scope 覆盖的那个资源**，不是请求里原样带来的字段。
	if previews.last.Resource["owner"] != "Runcoor" || previews.last.Resource["repo"] != "opendelo" {
		t.Errorf("查勘的资源为 %+v，期望 Scope 里的那一个", previews.last.Resource)
	}

	stored := harness.storedPreview(t, result.Request.ID)
	var changes []adapters.ResourceChange
	if err := json.Unmarshal([]byte(stored), &changes); err != nil {
		t.Fatalf("落库的查勘结果不是变化数组：%v（%s）", err, stored)
	}
	if len(changes) != 1 || changes[0].Before != "旧标题" {
		t.Errorf("落库的查勘结果为 %+v，期望带上旧值", changes)
	}
}

func TestDecide_PreviewFails_TheDecisionStandsAndTheFolioHasNoOldValue(t *testing.T) {
	// 第三条限定。一次查询失败让整条请求失败，等于把外部服务的可用性
	// 接进了决策链路 —— 结论已经写下、账本已经记了，查不到旧值只是少一栏。
	previews := &recordingPreviews{
		failWith: apperr.New(apperr.CodeGatewayUnavailable).WithDetail("外部服务不通"),
	}
	harness := newPreviewHarness(t, declaredWrite, previews)

	result := harness.submit(t, "create_issue", `{"title":"修一个空指针"}`)
	if result.Decision.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %q，期望 require_approval", result.Decision.Verdict)
	}
	if result.Approval == nil {
		t.Fatal("查勘失败连审批项都没产生 —— 它阻塞了决策")
	}
	if previews.calls != 1 {
		t.Errorf("查勘被调用了 %d 次，期望 1 次", previews.calls)
	}
	if stored := harness.storedPreview(t, result.Request.ID); stored != "" {
		t.Errorf("查勘失败却记下了旧值 %q", stored)
	}
}

func TestDecide_AutoAllowed_IsNeverPreviewed(t *testing.T) {
	// 直接放行的请求没有卷宗要看。为它发一次查勘等于凭空多一次带凭据的出站请求，
	// 而这次请求马上就要真的执行了 —— 旧值没有任何人会读到。
	previews := &recordingPreviews{}
	harness := newPreviewHarness(t, declaredRead, previews)

	result := harness.submit(t, "read_repository", "")
	// 前提要钉住：这条用例只有在结论确实是「直接放行」时才测得到那道闸。
	if result.Decision.Verdict != decision.VerdictAutoAllow {
		t.Fatalf("结论为 %q，期望 auto_allow", result.Decision.Verdict)
	}
	if previews.calls != 0 {
		t.Errorf("直接放行的请求也发出了 %d 次查勘", previews.calls)
	}
}

func TestDecide_DeleteWithoutADesiredChange_IsStillPreviewed(t *testing.T) {
	// 删除没有期望变更，却恰恰是最需要先看清当前值的那一种：点头之后那条记录
	// 就不在了。「有期望变更才查」这条闸会把它一起挡掉。
	previews := &recordingPreviews{changes: []adapters.ResourceChange{
		{Resource: "opendelo", Field: "name", Before: "opendelo", After: ""},
	}}
	harness := newPreviewHarness(t, declaredDelete, previews)

	result := harness.submit(t, "delete_repository", "")
	if result.Decision.Verdict != decision.VerdictRequireApproval {
		t.Fatalf("结论为 %q，期望 require_approval", result.Decision.Verdict)
	}
	if previews.calls != 1 {
		t.Fatalf("删除请求发出了 %d 次查勘，期望 1 次", previews.calls)
	}
	if harness.storedPreview(t, result.Request.ID) == "" {
		t.Error("删除请求的旧值没有落库")
	}
}

func TestDecide_RefusedRequest_IsNeverPreviewed(t *testing.T) {
	// 被拒的请求没有卷宗要看，而查勘是一次真实的出站请求。更要紧的是：
	// 这条路上身份可能根本没匹配上，那时连该用谁的凭据都不知道。
	previews := &recordingPreviews{}
	harness := newPreviewHarness(t, declaredRead, previews)

	result := harness.submit(t, "delete_repository", `{"confirm":true}`)
	if result.Request.Status != pipeline.StatusDenied {
		t.Fatalf("状态为 %q，期望 denied", result.Request.Status)
	}
	if result.Decision.Verdict == decision.VerdictRequireApproval {
		t.Fatal("被拒的请求却停在等人那一支")
	}
	if previews.calls != 0 {
		t.Errorf("被拒的请求也发出了 %d 次查勘", previews.calls)
	}
}
