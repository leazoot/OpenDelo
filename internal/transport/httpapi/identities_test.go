package httpapi_test

import (
	"encoding/csv"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/trust"
	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Identity / Trust Memory / Audit 端点的契约测试。
 */

// ——— GET /v1/identities ———

func TestListIdentities_ReturnsTheSeededIdentityWithoutAnyCredentialField(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/identities", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		Items []httpapi.IdentityView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("返回了 %d 个身份，期望 1 个", len(envelope.Items))
	}
	if envelope.Items[0].CredentialReferenceID == "" {
		t.Error("身份没有指向凭据引用，取不到凭据的身份匹配上了也执行不了")
	}

	// 「已连接的 Token 前四位」这类展示在响应里连位置都没有。
	body := strings.ToLower(response.Body.String())
	for _, banned := range []string{"token", "secret", "password", "authorization"} {
		if strings.Contains(body, banned) {
			t.Errorf("响应里出现了 %q", banned)
		}
	}
}

// TestListIdentities_CarriesTheConnectableServices 守连接表单的下拉数据源。
//
// 没有它，界面只能让用户自己猜服务名，而猜错要等提交才知道。
// 清单来自 Adapter 声明：没有 Adapter 的服务连上了也执行不了。
func TestListIdentities_CarriesTheConnectableServices(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/identities", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		ConnectableServices []string `json:"connectable_services"`
	}
	decodeInto(t, response, &envelope)

	if !slices.Contains(envelope.ConnectableServices, fixtures.DefaultServiceLabel) {
		t.Errorf("可连接的服务为 %v，不含 %q", envelope.ConnectableServices, fixtures.DefaultServiceLabel)
	}
	if !slices.IsSorted(envelope.ConnectableServices) {
		t.Errorf("可连接的服务没有排序：%v —— 下拉的顺序会随注册顺序变", envelope.ConnectableServices)
	}
}

func TestIdentityEndpoints_RefuseAgents(t *testing.T) {
	// 身份、自动化、账本都是人的配置面，Agent 一律看不到（REQ-DECIDE-004 的边界）。
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	cases := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/v1/identities"},
		{http.MethodPost, "/v1/identities/connect"},
		{http.MethodPost, "/v1/identities/" + fixtures.DefaultIdentityID + "/verify"},
		{http.MethodDelete, "/v1/identities/" + fixtures.DefaultIdentityID},
		{http.MethodGet, "/v1/trust-memories"},
		{http.MethodPatch, "/v1/trust-memories/anything"},
		{http.MethodDelete, "/v1/trust-memories/anything"},
		{http.MethodGet, "/v1/audit-events"},
		{http.MethodGet, "/v1/audit-events/anything"},
		{http.MethodGet, "/v1/audit-events/export"},
	}

	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.target, func(t *testing.T) {
			response := all.call(t, testCase.method, testCase.target, "")
			if response.Code != http.StatusForbidden {
				t.Fatalf("状态码为 %d，期望 403", response.Code)
			}
		})
	}
}

// ——— POST /v1/identities/connect ———

// connectBody 拼一份合法的连接请求，用例只写自己关心的差异。
//
// 账户名默认用 bot 而不是 work：夹具里已经有一个 github/work 的身份，
// 而 (service, account_label) 上有唯一索引。
func connectBody(overrides map[string]string) string {
	fields := map[string]string{
		"provider_kind":     "1password",
		"provider_label":    "个人保险库",
		"provider_item_ref": "op://Personal/GitHub Bot",
		"field":             "token",
		"service":           fixtures.DefaultServiceLabel,
		"account_label":     "bot",
		"environment":       "production",
	}
	for name, value := range overrides {
		fields[name] = value
	}

	pairs := make([]string, 0, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if fields[name] == "" {
			continue
		}
		pairs = append(pairs, strconv.Quote(name)+":"+strconv.Quote(fields[name]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// TestConnect_RegistersTheReferenceFromCoordinates 是本端点的主路径：
// 用户在 Identities 页面选定「哪个来源、哪个条目、哪个字段」，一次调用
// 建出来源、引用与身份（REQ-CRED-002 AC1、REQ-IDENT-001）。
//
// 三个坐标全是元数据，凭它们无法离线还原出任何 Secret（REQ-CRED-001）。
func TestConnect_RegistersTheReferenceFromCoordinates(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	response := all.call(t, http.MethodPost, "/v1/identities/connect", connectBody(nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.IdentityView
	decodeInto(t, response, &view)
	if view.Status != string(matcher.StatusOK) {
		t.Errorf("新身份状态为 %q，期望 ok", view.Status)
	}
	if view.CredentialReferenceID == "" {
		t.Fatal("身份没有指向任何凭据引用")
	}

	// 引用确实落了库，而且坐标就是请求里给的那三项。
	reference, err := all.backend.Services.Credentials.Reference(
		t.Context(), view.CredentialReferenceID)
	if err != nil {
		t.Fatalf("读取新建的凭据引用失败：%v", err)
	}
	if reference.ItemRef != "op://Personal/GitHub Bot" {
		t.Errorf("引用的条目坐标是 %q", reference.ItemRef)
	}
	if reference.Field != "token" {
		t.Errorf("引用的字段是 %q", reference.Field)
	}

	// 服务名仍然由引用决定而不是由请求体直接落到身份上 —— 两处各存一份，
	// 迟早会出现「引用指向 GitHub、身份自称 Cloudflare」的一条记录。
	if view.Service != reference.Service {
		t.Errorf("身份的服务是 %q，引用的是 %q", view.Service, reference.Service)
	}

	// 刚探测过才建的，健康状态不该是「从未验证过」。
	if reference.HealthStatus != credentials.HealthOK {
		t.Errorf("新引用的健康状态是 %q，期望 ok", reference.HealthStatus)
	}
	if reference.LastVerifiedAt.IsZero() {
		t.Error("新引用没有记下验证时刻")
	}
}

// TestConnect_DeclaresTheServiceItConnects_Regression 守的是 R-24：
// `service_adapters` 表原本没有任何填充路径。
//
// 四个 Adapter 只活在内存里，而决策链路用的能力映射表来自数据库。
// 全新安装后该表为空 —— MCP 每次调用都 `capability_not_offered`，
// 方向是安全的，但产品在真实安装上不可用。
//
// 落点是连接流程而不是启动：启动时批量写入等于替用户「连接」了四个
// 他没配过凭据的服务。
func TestConnect_DeclaresTheServiceItConnects_Regression(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	before, err := all.backend.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	if len(before) != 0 {
		t.Fatalf("连接之前库里就有 %d 条声明", len(before))
	}

	response := all.call(t, http.MethodPost, "/v1/identities/connect", connectBody(nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	after, err := all.backend.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	// 只写这一个。用户连的是 github，不该顺带把别的服务也「连接」上。
	if len(after) != 1 {
		t.Fatalf("落库了 %d 条声明，期望 1 条", len(after))
	}
	if after[0].Service != fixtures.DefaultServiceLabel {
		t.Errorf("落库的是 %q 的声明，期望 %q", after[0].Service, fixtures.DefaultServiceLabel)
	}
	if after[0].BaseURL == "" || after[0].DefaultRiskLevel == "" {
		t.Error("声明缺少出站地址或兜底风险等级 —— 那两项少一个这条声明就不能用")
	}
}

// TestConnect_TwiceOnTheSameService_DeclaresItOnce 守复用：声明表在服务名上
// 有唯一索引，第二次连接必须落到复用而不是撞库。
func TestConnect_TwiceOnTheSameService_DeclaresItOnce(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	if first := all.call(t, http.MethodPost, "/v1/identities/connect",
		connectBody(nil)); first.Code != http.StatusCreated {
		t.Fatalf("第一次连接的状态码为 %d，正文为 %s", first.Code, first.Body.String())
	}
	second := all.call(t, http.MethodPost, "/v1/identities/connect",
		connectBody(map[string]string{
			"provider_item_ref": "op://Personal/GitHub Deploy",
			"account_label":     "deploy",
		}))
	if second.Code != http.StatusCreated {
		t.Fatalf("第二次连接的状态码为 %d，正文为 %s", second.Code, second.Body.String())
	}

	declarations, err := all.backend.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	if len(declarations) != 1 {
		t.Errorf("同一个服务落库了 %d 条声明", len(declarations))
	}
}

// TestConnect_UnavailableProvider_WritesNothing_Regression 守 Fail Closed 的
// 一条：来源探不通时不能留下任何一行。
//
// 留下一份来源或引用意味着界面上会出现一个看起来已经连好、实际取不到凭据的身份，
// 那是执行期才会暴露的失败（`.claude/rules/backend.md` §7）。
func TestConnect_UnavailableProvider_WritesNothing_Regression(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithDownSource(t), httpapi.Caller{})

	before := countIdentities(t, all)

	response := all.call(t, http.MethodPost, "/v1/identities/connect", connectBody(nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码为 %d，期望 503，正文为 %s", response.Code, response.Body.String())
	}
	if code := decodeErrorCode(t, response); code != "provider_unavailable" {
		t.Errorf("错误码为 %q", code)
	}

	if after := countIdentities(t, all); after != before {
		t.Errorf("身份数从 %d 变成了 %d —— 探不通的来源不该留下任何一行", before, after)
	}

	// 服务声明也不该留下。一份 enabled 的声明会让这个服务出现在工具清单里，
	// 而用户那次连接是失败的 —— 那等于替他连接了一个他没配过凭据的服务。
	declarations, err := all.backend.Declarations.EnabledDeclarations(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取声明失败：%v", err)
	}
	if len(declarations) != 0 {
		t.Errorf("连接失败却留下了 %d 条服务声明", len(declarations))
	}
}

// TestConnect_WithNoSourceRegistered_IsRefused 是另一条 Fail Closed：
// 来源根本没登记与来源登记了但探不通，后果一样 —— 取不到凭据。
func TestConnect_WithNoSourceRegistered_IsRefused(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost, "/v1/identities/connect", connectBody(nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码为 %d，期望 503，正文为 %s", response.Code, response.Body.String())
	}
}

// TestConnect_RetryAfterAFailedIdentity_ReusesTheReference_Regression 守的是
// 重试这条路。
//
// 引用与身份不在同一个事务里 —— 前者在 credential 包、后者在 core 侧的仓储 ——
// 因此身份那一步失败会留下一份没人指向的引用。再连一次必须复用它，
// 否则第一次失败之后，这组坐标就再也连不上了（坐标上有唯一索引）。
func TestConnect_RetryAfterAFailedIdentity_ReusesTheReference_Regression(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	// 夹具里已经有一个 github/work 的身份，(service, account_label) 上有唯一索引，
	// 因此这一次会在身份那一步失败，而引用已经写进去了。
	failed := all.call(t, http.MethodPost, "/v1/identities/connect",
		connectBody(map[string]string{"account_label": "work"}))
	if failed.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409，正文为 %s", failed.Code, failed.Body.String())
	}

	retried := all.call(t, http.MethodPost, "/v1/identities/connect", connectBody(nil))
	if retried.Code != http.StatusCreated {
		t.Fatalf("重试的状态码为 %d，正文为 %s", retried.Code, retried.Body.String())
	}
}

// TestConnect_RejectsAnyFieldThatCouldCarryAPlaintextCredential 是一条哨兵：
// 明文从不经过 Web API（REQ-CRED-001）。端点读不懂的字段一律 400，
// 因此这些名字连被读到的机会都没有 —— 但这条性质必须有用例守着，
// 它是「请求体里塞一个 token 就能绕过引用」与「塞不进去」的分界。
func TestConnect_RejectsAnyFieldThatCouldCarryAPlaintextCredential(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	for _, name := range []string{
		"token", "secret", "password", "api_key", "credential", "value", "plaintext",
	} {
		t.Run(name, func(t *testing.T) {
			body := connectBody(nil)
			smuggled := body[:len(body)-1] + "," +
				strconv.Quote(name) + ":" + strconv.Quote(sentinel.SentinelToken) + "}"

			response := all.call(t, http.MethodPost, "/v1/identities/connect", smuggled)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400，正文为 %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), sentinel.SentinelToken) {
				t.Error("错误体把请求里的哨兵回显了出来")
			}
		})
	}
}

func TestConnect_RejectsAnIncompleteOrUnknownCoordinate(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	cases := map[string]struct {
		overrides map[string]string
		expected  int
		field     string
	}{
		"没给来源种类": {
			overrides: map[string]string{"provider_kind": ""},
			expected:  http.StatusBadRequest,
			field:     "provider_kind",
		},
		"来源种类本期不实现": {
			overrides: map[string]string{"provider_kind": "hashicorp-vault"},
			expected:  http.StatusBadRequest,
			field:     "provider_kind",
		},
		"没给条目坐标": {
			overrides: map[string]string{"provider_item_ref": ""},
			expected:  http.StatusBadRequest,
			field:     "provider_item_ref",
		},
		"没给字段名": {
			overrides: map[string]string{"field": ""},
			expected:  http.StatusBadRequest,
			field:     "field",
		},
		"服务没有对应的 Adapter": {
			overrides: map[string]string{"service": "myspace"},
			expected:  http.StatusBadRequest,
			field:     "service",
		},
		"环境认不出": {
			overrides: map[string]string{"environment": "staging"},
			expected:  http.StatusBadRequest,
			field:     "environment",
		},
		"没给账户名": {
			overrides: map[string]string{"account_label": ""},
			expected:  http.StatusBadRequest,
			field:     "account_label",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := all.call(t, http.MethodPost,
				"/v1/identities/connect", connectBody(testCase.overrides))
			if response.Code != testCase.expected {
				t.Fatalf("状态码为 %d，期望 %d，正文为 %s",
					response.Code, testCase.expected, response.Body.String())
			}
			// 400 必须来自入口的校验而不是数据库的 CHECK：后者也会是一个错误，
			// 但它说不出是哪个字段不对。
			if fields := errorFields(t, response); !slices.Contains(fields, testCase.field) {
				t.Errorf("错误体里的 fields 为 %v，没有指出 %s", fields, testCase.field)
			}
		})
	}
}

func countIdentities(t *testing.T, all api) int {
	t.Helper()

	identities, err := all.backend.Services.Pipeline.Identities(t.Context(), 200)
	if err != nil {
		t.Fatalf("读取身份列表失败：%v", err)
	}
	return len(identities)
}

// ——— POST /v1/identities/:id/verify ———

func TestVerify_WithNoProviderRegistered_IsRefusedAndLeavesTheStatusAlone(t *testing.T) {
	// 「探测不通不是错误而是结论」说的是**来源在、但这份凭据用不了**。
	// 这里一个来源都没登记，属于「凭据来源不可用」这条 Fail Closed，
	// 只能拒绝。
	all := newAPI(t)

	response := all.call(t, http.MethodPost,
		"/v1/identities/"+fixtures.DefaultIdentityID+"/verify", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码为 %d，期望 503，正文为 %s", response.Code, response.Body.String())
	}
	if code := decodeErrorCode(t, response); code != "provider_unavailable" {
		t.Errorf("错误码为 %q", code)
	}

	// 探不出结论时不改状态：改成 ok 是凭空放行，改成 needs_review 则会把
	// 「网关这边没配好」记成「这个身份出了问题」。
	still, err := all.backend.Identities.IdentityByID(t.Context(), fixtures.DefaultIdentityID)
	if err != nil {
		t.Fatalf("读取身份失败：%v", err)
	}
	if still.Status != matcher.StatusOK {
		t.Errorf("身份状态被改成了 %q", still.Status)
	}
}

// ——— DELETE /v1/identities/:id ———

func TestDisconnect_RevokesEveryLeaseAndInvalidatesEveryMemory(t *testing.T) {
	// REQ-IDENT-001 AC2：级联撤销全部 Lease 与 Trust Memory，并产生审计事件。
	all := newAPI(t)
	item := waiting(t, all)

	granted := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-project", "")
	if granted.Code != http.StatusOK {
		t.Fatalf("放行失败：%d %s", granted.Code, granted.Body.String())
	}
	var settled settlement
	decodeInto(t, granted, &settled)
	if settled.Lease == nil || settled.TrustMemoryID == "" {
		t.Fatal("这条用例需要一条 Lease 与一条记忆")
	}

	response := all.call(t, http.MethodDelete,
		"/v1/identities/"+fixtures.DefaultIdentityID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var revocation struct {
		Identity            httpapi.IdentityView `json:"identity"`
		RevokedLeases       int                  `json:"revoked_leases"`
		InvalidatedMemories int                  `json:"invalidated_memories"`
	}
	decodeInto(t, response, &revocation)
	if revocation.RevokedLeases != 1 || revocation.InvalidatedMemories != 1 {
		t.Errorf("收回了 %d 条 Lease、失效了 %d 条记忆，各期望 1 条",
			revocation.RevokedLeases, revocation.InvalidatedMemories)
	}
	if revocation.Identity.Status != string(matcher.StatusNeedsReview) {
		t.Errorf("身份状态为 %q，断开之后不该还能自动使用", revocation.Identity.Status)
	}

	assertLeaseCount(t, all, 0)

	// 失效的记忆读得到而不是消失，且带着原因（REQ-TRUST-004 AC2）。
	invalidated, err := all.backend.Memories.MemoriesByStatus(
		t.Context(), trust.StatusInvalidated, 10)
	if err != nil {
		t.Fatalf("列出失效记忆失败：%v", err)
	}
	if len(invalidated) != 1 || invalidated[0].InvalidationReason == "" {
		t.Errorf("失效记忆为 %v，期望 1 条且带着原因", invalidated)
	}

	assertAuditEvent(t, all, "lease.revoked")
}

func TestDisconnect_AuditIsWrittenBeforeTheLeaseIsClosed(t *testing.T) {
	// 顺序不能调换：先记录再收回。这里断言收回的那条 Lease 在账本里找得到，
	// 且它记的是 revoked 而不是别的状态。
	all := newAPI(t)
	item := waiting(t, all)

	granted := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")
	var settled settlement
	decodeInto(t, granted, &settled)

	if response := all.call(t, http.MethodDelete,
		"/v1/identities/"+fixtures.DefaultIdentityID, ""); response.Code != http.StatusOK {
		t.Fatalf("断开失败：%d", response.Code)
	}

	events, err := all.backend.Events.Events(t.Context(), zeroTime, 100)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	for _, event := range events {
		if event.LeaseID == settled.Lease.ID && string(event.Type) == "lease.revoked" {
			if string(event.LeaseStatus) != string(lease.StatusRevoked) {
				t.Errorf("账本记的 Lease 状态是 %q", event.LeaseStatus)
			}
			return
		}
	}
	t.Errorf("账本里没有这条 Lease 的撤销记录：%s", settled.Lease.ID)
}

func TestDisconnect_AnUnknownIdentityIsNotFound(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodDelete, "/v1/identities/01J000000000000000NOPE", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}

// ——— 导出与账本不含哨兵 ———

func TestExport_ContainsNoSentinelInAnyFormat(t *testing.T) {
	// REQ-AUDIT-004 AC2：导出内容经过与展示相同的脱敏。
	// 展示与导出走的是同一个 view 函数，这里三种格式一并扫哨兵。
	all := newAPI(t)
	item := waiting(t, all)
	all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-once", "")

	for _, format := range []string{"json", "jsonl", "csv"} {
		t.Run(format, func(t *testing.T) {
			response := all.call(t, http.MethodGet, "/v1/audit-events/export?format="+format, "")
			if response.Code != http.StatusOK {
				t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
			}
			for _, value := range []string{
				sentinel.SentinelToken, sentinel.SentinelPassword,
				sentinel.SentinelAPIKey, sentinel.SentinelPrivateKey,
			} {
				if strings.Contains(response.Body.String(), value) {
					t.Errorf("导出内容里出现了哨兵 %q", value)
				}
			}
			if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(
				disposition, "opendelo-ledger."+format) {
				t.Errorf("Content-Disposition 为 %q", disposition)
			}
		})
	}
}

func TestExport_EveryFormatCarriesTheSameRecordCount(t *testing.T) {
	// REQ-AUDIT-004 AC1：三种格式导出的条数与当前过滤条件下的查询结果一致。
	all := newAPI(t)
	waiting(t, all)

	listed := all.call(t, http.MethodGet, "/v1/audit-events", "")
	var envelope struct {
		Items []httpapi.AuditEventView `json:"items"`
	}
	decodeInto(t, listed, &envelope)
	if len(envelope.Items) == 0 {
		t.Fatal("账本是空的，这条用例测不到条数一致")
	}

	jsonl := all.call(t, http.MethodGet, "/v1/audit-events/export?format=jsonl", "")
	if lines := countLines(jsonl.Body.String()); lines != len(envelope.Items) {
		t.Errorf("JSONL 有 %d 行，列表有 %d 条", lines, len(envelope.Items))
	}

	exported := all.call(t, http.MethodGet, "/v1/audit-events/export?format=csv", "")
	rows, err := csv.NewReader(strings.NewReader(exported.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("CSV 无法解析：%v", err)
	}
	if len(rows)-1 != len(envelope.Items) {
		t.Errorf("CSV 有 %d 条记录，列表有 %d 条", len(rows)-1, len(envelope.Items))
	}
	if len(rows[0]) != 23 {
		t.Errorf("CSV 表头有 %d 列，AuditEventView 有 23 个字段", len(rows[0]))
	}
}

func TestExport_UnknownFormatIsRefused(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/audit-events/export?format=xlsx", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func countLines(text string) int {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func TestListEvents_FiltersByAgentAndByService(t *testing.T) {
	// REQ-AUDIT-003 AC1：过滤结果与直接数据库查询一致。
	// 只支持这两个过滤，各自对应一个既有索引 —— 组合起来会退化成全表扫描。
	all := newAPI(t)
	waiting(t, all)

	byAgent := all.call(t, http.MethodGet,
		"/v1/audit-events?agent_id="+fixtures.DefaultAgentID, "")
	var mine struct {
		Items []httpapi.AuditEventView `json:"items"`
	}
	decodeInto(t, byAgent, &mine)
	if len(mine.Items) == 0 {
		t.Fatal("按 Agent 过滤一条都没有")
	}
	for _, event := range mine.Items {
		if event.AgentID != fixtures.DefaultAgentID {
			t.Errorf("过滤结果里混进了 Agent %q 的记录", event.AgentID)
		}
	}

	stranger := all.call(t, http.MethodGet, "/v1/audit-events?agent_id=agent-nobody", "")
	var none struct {
		Items []httpapi.AuditEventView `json:"items"`
	}
	decodeInto(t, stranger, &none)
	if len(none.Items) != 0 {
		t.Errorf("不存在的 Agent 返回了 %d 条", len(none.Items))
	}

	both := all.call(t, http.MethodGet,
		"/v1/audit-events?agent_id=a&service=github", "")
	if both.Code != http.StatusBadRequest {
		t.Errorf("两个过滤一起用返回 %d，期望 400", both.Code)
	}
}

func TestListEvents_MalformedCursorIsRefused(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/audit-events?before=昨天", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestListEvents_CarriesACursorForTheNextPage(t *testing.T) {
	all := newAPI(t)
	waiting(t, all)

	response := all.call(t, http.MethodGet, "/v1/audit-events?limit=1", "")
	var envelope struct {
		Items      []httpapi.AuditEventView `json:"items"`
		NextCursor string                   `json:"next_cursor"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("limit=1 返回了 %d 条", len(envelope.Items))
	}
	if envelope.NextCursor == "" {
		t.Error("有记录却没有给出下一页的游标")
	}
}

func TestShowEvent_ReturnsOneRecordAnd404ForAnUnknownID(t *testing.T) {
	all := newAPI(t)
	waiting(t, all)

	listed := all.call(t, http.MethodGet, "/v1/audit-events?limit=1", "")
	var envelope struct {
		Items []httpapi.AuditEventView `json:"items"`
	}
	decodeInto(t, listed, &envelope)

	one := all.call(t, http.MethodGet, "/v1/audit-events/"+envelope.Items[0].ID, "")
	if one.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", one.Code, one.Body.String())
	}
	var view httpapi.AuditEventView
	decodeInto(t, one, &view)
	if view.ID != envelope.Items[0].ID {
		t.Errorf("读回的是 %s", view.ID)
	}
	if view.OperationID == "" {
		t.Error("账本条目没有 operation_id，这次操作无从追溯")
	}

	missing := all.call(t, http.MethodGet, "/v1/audit-events/01J000000000000000NOPE", "")
	if missing.Code != http.StatusNotFound {
		t.Errorf("不存在的 id 返回 %d，期望 404", missing.Code)
	}
}

func TestLedger_ServiceFilterAndExportShareTheSameQuery(t *testing.T) {
	// 导出与展示走同一条取数路径，因此同一个过滤条件下两边条数必须一致。
	all := newAPI(t)
	waiting(t, all)

	const target = "/v1/audit-events?service=" + fixtures.DefaultServiceLabel
	listed := all.call(t, http.MethodGet, target, "")
	var envelope struct {
		Items []httpapi.AuditEventView `json:"items"`
	}
	decodeInto(t, listed, &envelope)
	if len(envelope.Items) == 0 {
		t.Fatal("按服务过滤一条都没有")
	}

	exported := all.call(t, http.MethodGet,
		"/v1/audit-events/export?format=jsonl&service="+fixtures.DefaultServiceLabel, "")
	if lines := countLines(exported.Body.String()); lines != len(envelope.Items) {
		t.Errorf("导出 %d 行，列表 %d 条", lines, len(envelope.Items))
	}

	// 过滤到一个没有任何记录的 Agent：导出必须跟着空掉。
	// 用同服务过滤对比测不出这一点 —— 夹具里的记录服务名本来就都一样。
	empty := all.call(t, http.MethodGet,
		"/v1/audit-events/export?format=jsonl&agent_id=agent-nobody", "")
	if empty.Code != http.StatusOK {
		t.Fatalf("按 Agent 导出返回 %d", empty.Code)
	}
	if lines := countLines(empty.Body.String()); lines != 0 {
		t.Errorf("过滤到一个没有记录的 Agent，导出仍有 %d 行", lines)
	}
}

func TestConfigurationEndpoints_RejectAnUnusableLimit(t *testing.T) {
	// 认不出的 limit 一律拒绝，不悄悄换成默认值 —— 三个列表端点行为一致。
	all := newAPI(t)

	for _, target := range []string{
		"/v1/identities?limit=0",
		"/v1/trust-memories?limit=999",
		"/v1/audit-events?limit=abc",
		"/v1/audit-events/export?limit=-1",
	} {
		t.Run(target, func(t *testing.T) {
			if response := all.call(t, http.MethodGet, target, ""); response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
		})
	}
}

func TestWriteEndpoints_RejectAMalformedBody(t *testing.T) {
	all := newAPI(t)

	cases := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/v1/identities/connect", `{"credential_reference_id":`},
		{http.MethodPatch, "/v1/trust-memories/anything", `不是 JSON`},
		{http.MethodPost, "/v1/identities/connect", `{"unknown_field":1}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.target+" "+testCase.body, func(t *testing.T) {
			response := all.call(t, testCase.method, testCase.target, testCase.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
		})
	}
}

func TestVerify_WithAHealthySource_MarksTheIdentityOK(t *testing.T) {
	// 探测通过时身份回到 ok，自动授权可以继续（REQ-CRED-005）。
	all := newAPIWith(t, fixtures.NewGatewayWithHealthySource(t), httpapi.Caller{})

	// 先把身份压成「需要检查」，才能看出这次验证确实把它抬回来了。
	if _, err := all.backend.Identities.SetIdentityStatus(t.Context(),
		fixtures.DefaultIdentityID, matcher.StatusNeedsReview, fixtures.Instant); err != nil {
		t.Fatalf("设置身份状态失败：%v", err)
	}

	response := all.call(t, http.MethodPost,
		"/v1/identities/"+fixtures.DefaultIdentityID+"/verify", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		Identity httpapi.IdentityView `json:"identity"`
		Health   string               `json:"health"`
	}
	decodeInto(t, response, &envelope)
	if envelope.Identity.Status != string(matcher.StatusOK) {
		t.Errorf("身份状态为 %q，期望 ok", envelope.Identity.Status)
	}
	if envelope.Health != "ok" {
		t.Errorf("健康状态为 %q，期望 ok", envelope.Health)
	}

	// 来源返回的是哨兵明文，而它一个字节都不该出现在响应里。
	if strings.Contains(response.Body.String(), sentinel.SentinelToken) {
		t.Error("响应里出现了凭据哨兵")
	}
}
