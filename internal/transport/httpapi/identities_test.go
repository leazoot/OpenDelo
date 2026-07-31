package httpapi_test

import (
	"encoding/csv"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/trust"
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

func TestConnect_TakesAReferenceNotAPlaintextCredential(t *testing.T) {
	// 请求体里只有一个引用 id。明文从不经过 Web API（REQ-CRED-001）。
	all := newAPI(t)

	body := `{"credential_reference_id":"` + fixtures.DefaultReferenceID + `",` +
		`"account_label":"personal","environment":"non-production","is_default":false}`
	response := all.call(t, http.MethodPost, "/v1/identities/connect", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.IdentityView
	decodeInto(t, response, &view)
	if view.Status != string(matcher.StatusOK) {
		t.Errorf("新身份状态为 %q，期望 ok", view.Status)
	}

	// 服务名由引用决定，不由请求体决定 —— 否则能造出
	// 「引用指向 GitHub、身份自称 Cloudflare」的一条记录。
	reference, err := all.backend.Services.Credentials.Reference(
		t.Context(), fixtures.DefaultReferenceID)
	if err != nil {
		t.Fatalf("读取凭据引用失败：%v", err)
	}
	if view.Service != reference.Service {
		t.Errorf("身份的服务是 %q，引用的是 %q", view.Service, reference.Service)
	}
}

func TestConnect_RejectsAMissingOrUnknownReference(t *testing.T) {
	all := newAPI(t)

	cases := map[string]struct {
		body     string
		expected int
		field    string
	}{
		"没给引用": {
			body:     `{"account_label":"work","environment":"production"}`,
			expected: http.StatusBadRequest,
			field:    "credential_reference_id",
		},
		"引用不存在": {
			body: `{"credential_reference_id":"01J000000000000000NOPE",` +
				`"account_label":"work","environment":"production"}`,
			expected: http.StatusNotFound,
		},
		"环境认不出": {
			body: `{"credential_reference_id":"` + fixtures.DefaultReferenceID + `",` +
				`"account_label":"work","environment":"staging"}`,
			expected: http.StatusBadRequest,
			field:    "environment",
		},
		"没给账户名": {
			body: `{"credential_reference_id":"` + fixtures.DefaultReferenceID + `",` +
				`"environment":"production"}`,
			expected: http.StatusBadRequest,
			field:    "account_label",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := all.call(t, http.MethodPost, "/v1/identities/connect", testCase.body)
			if response.Code != testCase.expected {
				t.Fatalf("状态码为 %d，期望 %d，正文为 %s",
					response.Code, testCase.expected, response.Body.String())
			}
			if testCase.field == "" {
				return
			}
			// 400 必须来自入口的校验而不是数据库的 CHECK：后者也会是一个错误，
			// 但它说不出是哪个字段不对。
			if fields := errorFields(t, response); !slices.Contains(fields, testCase.field) {
				t.Errorf("错误体里的 fields 为 %v，没有指出 %s", fields, testCase.field)
			}
		})
	}
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
