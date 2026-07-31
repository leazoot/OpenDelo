package repo_test

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// requestChain 是请求链上的三个仓储，共用同一个已迁移的数据库。
type requestChain struct {
	db        *store.DB
	adapters  *repo.ServiceAdapters
	requests  *repo.CapabilityRequests
	decisions *repo.Decisions
}

func newRequestChain(t *testing.T) requestChain {
	t.Helper()

	db := fixtures.MigratedDB(t)
	return requestChain{
		db:        db,
		adapters:  repo.NewServiceAdapters(db),
		requests:  repo.NewCapabilityRequests(db),
		decisions: repo.NewDecisions(db),
	}
}

// seededRequestChain 准备好 Agent 与工作区，使能力请求可以写入。
func seededRequestChain(t *testing.T) requestChain {
	t.Helper()

	chain := newRequestChain(t)
	ctx := t.Context()
	if _, err := repo.NewDevices(chain.db).CreateDevice(ctx, fixtures.Device()); err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}
	if _, err := repo.NewWorkspaces(chain.db).CreateWorkspace(ctx, fixtures.Workspace()); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}
	if _, err := repo.NewAgents(chain.db).CreateAgent(ctx, fixtures.Agent(
		fixtures.DefaultDeviceID, fixtures.DefaultWorkspaceID)); err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	return chain
}

// seededDecisionChain 在请求之外再准备好凭据链与身份，使决策可以带上匹配结果。
func seededDecisionChain(t *testing.T) requestChain {
	t.Helper()

	chain := seededRequestChain(t)
	ctx := t.Context()
	if _, err := repo.NewCredentialProviders(chain.db).CreateProvider(ctx, fixtures.Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	if _, err := repo.NewCredentialReferences(chain.db).CreateReference(ctx, fixtures.Reference()); err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
	if _, err := repo.NewIdentities(chain.db).CreateIdentity(ctx, fixtures.Identity()); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	return chain
}

func TestServiceAdapters_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := newRequestChain(t)
	want := fixtures.Declaration()

	created, err := chain.adapters.CreateDeclaration(ctx, want)
	if err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.adapters.DeclarationByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byService, err := chain.adapters.DeclarationByService(ctx, want.Service)
	if err != nil {
		t.Fatalf("按服务名读取失败：%v", err)
	}
	if byService != want {
		t.Errorf("按服务名读到 %+v，期望 %+v", byService, want)
	}
}

func TestServiceAdapters_UnknownService_ReportsNotFound(t *testing.T) {
	// 查不到声明即 Adapter 未声明能力，决策链路据此拒绝（Fail Closed）。
	chain := newRequestChain(t)

	_, err := chain.adapters.DeclarationByService(t.Context(), "vercel")
	assertCode(t, err, apperr.CodeNotFound)
}

func TestServiceAdapters_DuplicateService_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	chain := newRequestChain(t)

	if _, err := chain.adapters.CreateDeclaration(ctx, fixtures.Declaration()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}
	_, err := chain.adapters.CreateDeclaration(ctx,
		fixtures.Declaration(fixtures.WithDeclarationID("01K1ADAPTER000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestServiceAdapters_UnimplementedKind_ReportsInvalidRequest(t *testing.T) {
	// REQ-ADAPTER-006 AC1：注册表只含本期实现的四种。
	chain := newRequestChain(t)

	_, err := chain.adapters.CreateDeclaration(t.Context(),
		fixtures.Declaration(fixtures.WithDeclarationKind(adapters.Kind("vercel"))))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestServiceAdapters_NonJSONCapabilities_ReportsInvalidRequest(t *testing.T) {
	chain := newRequestChain(t)

	_, err := chain.adapters.CreateDeclaration(t.Context(),
		fixtures.Declaration(fixtures.WithDeclarationCapabilities("not json")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestServiceAdapters_DisabledDeclaration_LeavesTheEnabledList(t *testing.T) {
	// 停用而不是删除：删掉声明会让账本里的历史请求失去解释。
	ctx := t.Context()
	chain := newRequestChain(t)
	if _, err := chain.adapters.CreateDeclaration(ctx, fixtures.Declaration()); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	enabled, err := chain.adapters.EnabledDeclarations(ctx, 10)
	if err != nil {
		t.Fatalf("列出启用中的声明失败：%v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("启用中的声明有 %d 条，期望 1 条", len(enabled))
	}

	disabled, err := chain.adapters.SetDeclarationStatus(ctx,
		fixtures.DefaultAdapterID, adapters.StatusDisabled, fixtures.Instant)
	if err != nil {
		t.Fatalf("停用声明失败：%v", err)
	}
	if disabled.Status != adapters.StatusDisabled {
		t.Errorf("停用后状态是 %q，期望 disabled", disabled.Status)
	}

	enabled, err = chain.adapters.EnabledDeclarations(ctx, 10)
	if err != nil {
		t.Fatalf("列出启用中的声明失败：%v", err)
	}
	if len(enabled) != 0 {
		t.Errorf("停用后仍列出了 %d 条声明", len(enabled))
	}

	// 停用的声明仍然读得到，账本因此仍能解释历史请求。
	if _, err := chain.adapters.DeclarationByService(ctx, fixtures.DefaultServiceLabel); err != nil {
		t.Errorf("停用的声明读不到了：%v", err)
	}
}

func TestServiceAdapters_Update_LeavesServiceAndKindUntouched(t *testing.T) {
	// Service 与 Kind 是这条声明的身份，改了就是另一个 Adapter，
	// 而历史请求还指着旧的服务名。
	ctx := t.Context()
	chain := newRequestChain(t)
	if _, err := chain.adapters.CreateDeclaration(ctx, fixtures.Declaration()); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	attempt := fixtures.Declaration(
		fixtures.WithDeclarationService("cloudflare"),
		fixtures.WithDeclarationKind(adapters.KindCloudflare),
		fixtures.WithDeclarationRiskLabel(adapters.RiskLabelHigh),
	)
	updated, err := chain.adapters.UpdateDeclaration(ctx, attempt)
	if err != nil {
		t.Fatalf("更新 Adapter 声明失败：%v", err)
	}

	if updated.Service != fixtures.DefaultServiceLabel {
		t.Errorf("服务名被改成了 %q", updated.Service)
	}
	if updated.Kind != adapters.KindGitHub {
		t.Errorf("种类被改成了 %q", updated.Kind)
	}
	if updated.DefaultRiskLevel != adapters.RiskLabelHigh {
		t.Errorf("风险标签是 %q，期望 high", updated.DefaultRiskLevel)
	}
}

func TestServiceAdapters_EnabledDeclarations_RejectsNonPositiveLimit(t *testing.T) {
	chain := newRequestChain(t)

	for _, limit := range []int{0, -1} {
		_, err := chain.adapters.EnabledDeclarations(t.Context(), limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestCapabilityRequests_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)
	want := fixtures.Request()

	created, err := chain.requests.CreateRequest(ctx, want)
	if err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.requests.RequestByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestCapabilityRequests_ReadOperation_StoresDesiredChangeAsNull(t *testing.T) {
	// 读操作没有期望变更。空对象会在审批页面上被读成「变更为空」，那是另一句话。
	ctx := t.Context()
	chain := seededRequestChain(t)

	created, err := chain.requests.CreateRequest(ctx,
		fixtures.Request(fixtures.WithRequestDesiredChange("")))
	if err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	if created.DesiredChange != "" {
		t.Errorf("期望变更是 %q，期望为空", created.DesiredChange)
	}

	var stored sql.NullString
	if err := chain.db.Reader().QueryRowContext(ctx,
		`SELECT desired_change FROM capability_requests WHERE id = ?`,
		fixtures.DefaultRequestID).Scan(&stored); err != nil {
		t.Fatalf("直接读取期望变更失败：%v", err)
	}
	if stored.Valid {
		t.Errorf("库里存的是 %q，期望 NULL", stored.String)
	}
}

func TestCapabilityRequests_SaveChangePreview_OnlyLandsOnARequestStillWaiting(t *testing.T) {
	// 查勘与人做决定是并行的。请求已经有了结论时这次写入必须落空 ——
	// 否则卷宗上的旧值会在决定之后被换掉，而那正是这次决定的依据。
	ctx := t.Context()
	chain := seededRequestChain(t)

	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request(
		fixtures.WithRequestID("01K1REQUEST000000000000009"),
		fixtures.WithRequestStatus(pipeline.StatusAwaitingApproval),
	)); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	const preview = `[{"resource":"opendelo","field":"title","before":"旧","after":"新"}]`
	if err := chain.requests.SaveChangePreview(ctx, "01K1REQUEST000000000000009",
		preview, pipeline.StatusAwaitingApproval, fixtures.Instant); err != nil {
		t.Fatalf("写入查勘结果失败：%v", err)
	}

	stored, err := chain.requests.RequestByID(ctx, "01K1REQUEST000000000000009")
	if err != nil {
		t.Fatalf("读回能力请求失败：%v", err)
	}
	if stored.ChangePreview != preview {
		t.Errorf("落库的查勘结果为 %q，期望 %q", stored.ChangePreview, preview)
	}

	// 请求被决定之后，同一次写入落空并报冲突。
	if _, err = chain.requests.AdvanceRequest(ctx, "01K1REQUEST000000000000009",
		pipeline.StatusAwaitingApproval, pipeline.StatusApproved, fixtures.Instant); err != nil {
		t.Fatalf("推进状态失败：%v", err)
	}
	err = chain.requests.SaveChangePreview(ctx, "01K1REQUEST000000000000009",
		`[{"resource":"opendelo","field":"title","before":"事后改的","after":"新"}]`,
		pipeline.StatusAwaitingApproval, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)

	settled, err := chain.requests.RequestByID(ctx, "01K1REQUEST000000000000009")
	if err != nil {
		t.Fatalf("读回能力请求失败：%v", err)
	}
	if settled.ChangePreview != preview {
		t.Errorf("决定之后旧值被改成了 %q", settled.ChangePreview)
	}
}

func TestCapabilityRequests_NeverPreviewed_StoresNull(t *testing.T) {
	// 「没有查过」与「查过但没有可对照的字段」在卷宗上是两句不同的话。
	ctx := t.Context()
	chain := seededRequestChain(t)
	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	var stored sql.NullString
	if err := chain.db.Reader().QueryRowContext(ctx,
		`SELECT change_preview FROM capability_requests WHERE id = ?`,
		fixtures.DefaultRequestID).Scan(&stored); err != nil {
		t.Fatalf("直接读取查勘结果失败：%v", err)
	}
	if stored.Valid {
		t.Errorf("库里存的是 %q，期望 NULL", stored.String)
	}
}

func TestCapabilityRequests_ByStatus_ReturnsArrivalOrder(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)

	// 后到的请求先写入，用来证明排序来自 created_at 而不是写入顺序。
	later := fixtures.Request(
		fixtures.WithRequestID("01K1REQUEST000000000000002"),
		fixtures.WithRequestStatus(pipeline.StatusAwaitingApproval),
	)
	later.CreatedAt = fixtures.Instant.Add(time.Minute)
	later.UpdatedAt = later.CreatedAt
	if _, err := chain.requests.CreateRequest(ctx, later); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	earlier := fixtures.Request(
		fixtures.WithRequestID("01K1REQUEST000000000000001"),
		fixtures.WithRequestStatus(pipeline.StatusAwaitingApproval),
	)
	if _, err := chain.requests.CreateRequest(ctx, earlier); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
	// 另一个状态的请求不应出现在待审批列表里。
	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request(
		fixtures.WithRequestID("01K1REQUEST000000000000003"),
		fixtures.WithRequestStatus(pipeline.StatusDenied),
	)); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	pending, err := chain.requests.RequestsByStatus(ctx, pipeline.StatusAwaitingApproval, 10)
	if err != nil {
		t.Fatalf("列出待审批请求失败：%v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("待审批请求有 %d 条，期望 2 条", len(pending))
	}
	if pending[0].ID != earlier.ID || pending[1].ID != later.ID {
		t.Errorf("待审批顺序是 %q, %q，期望先到的在前", pending[0].ID, pending[1].ID)
	}
}

func TestCapabilityRequests_ByStatus_RejectsNonPositiveLimit(t *testing.T) {
	chain := seededRequestChain(t)

	for _, limit := range []int{0, -1} {
		_, err := chain.requests.RequestsByStatus(t.Context(), pipeline.StatusAwaitingApproval, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestCapabilityRequests_Advance_OnlyMovesFromTheExpectedStatus(t *testing.T) {
	ctx := t.Context()
	chain := seededRequestChain(t)
	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	advanced, err := chain.requests.AdvanceRequest(ctx, fixtures.DefaultRequestID,
		pipeline.StatusReceived, pipeline.StatusResolving, fixtures.Instant.Add(time.Second))
	if err != nil {
		t.Fatalf("推进状态失败：%v", err)
	}
	if advanced.Status != pipeline.StatusResolving {
		t.Errorf("推进后状态是 %q，期望 resolving", advanced.Status)
	}
	if !advanced.UpdatedAt.Equal(fixtures.Instant.Add(time.Second)) {
		t.Errorf("更新时间是 %v，期望随推进一起变化", advanced.UpdatedAt)
	}

	// 起点状态已经变了，同样的推进不能再成功。
	_, err = chain.requests.AdvanceRequest(ctx, fixtures.DefaultRequestID,
		pipeline.StatusReceived, pipeline.StatusResolving, fixtures.Instant)
	assertCode(t, err, apperr.CodeConflict)
}

func TestCapabilityRequests_ConcurrentAdvance_SucceedsOnlyOnce(t *testing.T) {
	// 同一条请求被两个 goroutine 同时推进时，只能有一个成功。
	// 条件更新把这件事交给数据库，读回来再判断挡不住这种竞态。
	ctx := t.Context()
	chain := seededRequestChain(t)
	if _, err := chain.requests.CreateRequest(ctx, fixtures.Request()); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}

	const racers = 8
	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
		succeeded int
	)
	waitGroup.Add(racers)
	for range racers {
		go func() {
			defer waitGroup.Done()
			_, err := chain.requests.AdvanceRequest(ctx, fixtures.DefaultRequestID,
				pipeline.StatusReceived, pipeline.StatusResolving, fixtures.Instant)
			if err == nil {
				mutex.Lock()
				succeeded++
				mutex.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if succeeded != 1 {
		t.Errorf("%d 个并发推进中有 %d 个成功，期望恰好 1 个", racers, succeeded)
	}
}

func TestCapabilityRequests_UnknownAgent_ReportsInvalidRequest(t *testing.T) {
	chain := seededRequestChain(t)

	_, err := chain.requests.CreateRequest(t.Context(),
		fixtures.Request(fixtures.WithRequestAgentID("01K1MISSING00000000000000")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestDecisions_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := seededDecisionChain(t)
	want := fixtures.Decision()

	created, err := chain.decisions.CreateDecision(ctx, want)
	if err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.decisions.DecisionByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byRequest, err := chain.decisions.DecisionByRequestID(ctx, want.CapabilityRequestID)
	if err != nil {
		t.Fatalf("按请求读取失败：%v", err)
	}
	if byRequest != want {
		t.Errorf("按请求读到 %+v，期望 %+v", byRequest, want)
	}
}

func TestDecisions_WithoutMatchedIdentity_RoundTripsAsEmpty(t *testing.T) {
	// 没匹配到身份正是 Fail Closed 要拒绝的情况之一，
	// 决策记录必须能如实表达它，而不是填一个假的身份。
	ctx := t.Context()
	chain := seededDecisionChain(t)

	denied := fixtures.Decision(
		fixtures.WithDecisionVerdict(decision.VerdictDeny),
		fixtures.WithDecisionRiskLevel(risk.LevelHigh),
		fixtures.WithDecisionMatch("", ""),
	)
	created, err := chain.decisions.CreateDecision(ctx, denied)
	if err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	if created.IdentityID != "" || created.MatchLevel != "" {
		t.Errorf("身份是 %q、层级是 %q，期望都为空", created.IdentityID, created.MatchLevel)
	}

	var identityID, matchLevel sql.NullString
	if err := chain.db.Reader().QueryRowContext(ctx,
		`SELECT identity_id, match_level FROM decisions WHERE id = ?`,
		fixtures.DefaultDecisionID).Scan(&identityID, &matchLevel); err != nil {
		t.Fatalf("直接读取决策失败：%v", err)
	}
	if identityID.Valid || matchLevel.Valid {
		t.Error("库里存的不是 NULL")
	}
}

func TestDecisions_IdentityWithoutMatchLevel_ReportsInvalidRequest(t *testing.T) {
	// 匹配到身份却说不出命中哪一层，这条记录本身就是坏的（REQ-IDENT-002 AC3）。
	ctx := t.Context()
	chain := seededDecisionChain(t)

	_, err := chain.decisions.CreateDecision(ctx,
		fixtures.Decision(fixtures.WithDecisionMatch(fixtures.DefaultIdentityID, "")))
	assertCode(t, err, apperr.CodeInvalidRequest)

	_, err = chain.decisions.CreateDecision(ctx,
		fixtures.Decision(fixtures.WithDecisionMatch("", matcher.MatchSoleIdentity)))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestDecisions_SecondDecisionForTheSameRequest_ReportsConflict(t *testing.T) {
	// REQ-API-004 的幂等在存储层的保证：重复决策拿到冲突而不是第二个结论。
	ctx := t.Context()
	chain := seededDecisionChain(t)

	if _, err := chain.decisions.CreateDecision(ctx, fixtures.Decision()); err != nil {
		t.Fatalf("首次写入决策失败：%v", err)
	}
	_, err := chain.decisions.CreateDecision(ctx,
		fixtures.Decision(
			fixtures.WithDecisionID("01K1DECISION00000000000002"),
			fixtures.WithDecisionVerdict(decision.VerdictAutoAllow),
		))
	assertCode(t, err, apperr.CodeConflict)
}

func TestDecisions_UnknownRequest_ReportsInvalidRequest(t *testing.T) {
	chain := seededDecisionChain(t)

	_, err := chain.decisions.CreateDecision(t.Context(),
		fixtures.Decision(fixtures.WithDecisionRequestID("01K1MISSING00000000000000")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestDecisions_MissingRow_ReportsNotFound(t *testing.T) {
	chain := seededDecisionChain(t)

	_, err := chain.decisions.DecisionByRequestID(t.Context(), fixtures.DefaultRequestID)
	assertCode(t, err, apperr.CodeNotFound)
}

func TestRequestChain_ThroughTheCoreInterfaces_Works(t *testing.T) {
	// 仓储必须能以 core 侧声明的接口使用 —— 依赖倒置只有在这条路径上成立才算数。
	ctx := t.Context()
	chain := seededDecisionChain(t)

	var (
		requests  pipeline.CapabilityRequestRepository = chain.requests
		decisions decision.DecisionRepository          = chain.decisions
		registrar adapters.DeclarationRepository       = chain.adapters
	)

	if _, err := registrar.CreateDeclaration(ctx, fixtures.Declaration()); err != nil {
		t.Fatalf("经接口写入 Adapter 声明失败：%v", err)
	}
	if _, err := decisions.CreateDecision(ctx, fixtures.Decision()); err != nil {
		t.Fatalf("经接口写入决策失败：%v", err)
	}

	stored, err := requests.RequestByID(ctx, fixtures.DefaultRequestID)
	if err != nil {
		t.Fatalf("经接口读取能力请求失败：%v", err)
	}
	if stored.Service != fixtures.DefaultServiceLabel {
		t.Errorf("读到的服务是 %q，期望 %q", stored.Service, fixtures.DefaultServiceLabel)
	}
}
