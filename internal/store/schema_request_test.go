package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * service_adapters / capability_requests / decisions 三张表的约束检查。
 */

const (
	testAdapterID  = "01K1ADAPTERAAAAAAAAAAAAAAA"
	testRequestID  = "01K1REQUESTAAAAAAAAAAAAAAA"
	testDecisionID = "01K1DECISIONAAAAAAAAAAAAAA"
	testAgentID    = "01K1AGENTAAAAAAAAAAAAAAAAA"
)

// requestStatuses 是 REQ-CAP-001 状态机的全部取值。
// 状态机图里出现的每一个状态都必须能被存下来，否则实现会在某一步写不进去。
var requestStatuses = []string{
	"received", "resolving", "deciding",
	"auto_allowed", "awaiting_approval", "denied",
	"approved", "rejected", "expired", "cancelled",
	"executing", "succeeded", "failed",
}

func insertAdapter(ctx context.Context, db *DB, id, service, kind, riskLevel, capabilities string) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO service_adapters (
			id, service, kind, display_name, base_url, auth_scheme,
			capabilities, allowed_paths, allowed_methods, redaction_rules,
			default_risk_level, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'GitHub', 'https://api.github.com', 'bearer',
			?, '["/repos/*"]', '["GET"]', '["authorization"]', ?, 'enabled', ?, ?)`,
		id, service, kind, capabilities, riskLevel, testInstant, testInstant)
	return err
}

func insertRequest(ctx context.Context, db *DB, id, status, resource string, desiredChange any) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO capability_requests (
			id, operation_id, agent_id, workspace_id, service, operation,
			resource, desired_change, reason, status, created_at, updated_at)
		 VALUES (?, '01K1OPERATION', ?, ?, 'github', 'pull_request.create',
			?, ?, 'Open the release pull request', ?, ?, ?)`,
		id, testAgentID, testWorkspaceID, resource, desiredChange, status, testInstant, testInstant)
	return err
}

func insertDecision(
	ctx context.Context, db *DB, id, verdict, riskLevel, requirement string, identityID, matchLevel any,
) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO decisions (
			id, capability_request_id, verdict, risk_level, risk_factors,
			identity_id, match_level, resolved_scope, approval_requirement,
			reason_code, created_at)
		 VALUES (?, ?, ?, ?, '["write"]', ?, ?, '{"service":"github"}', ?, 'risk_requires_confirmation', ?)`,
		id, testRequestID, verdict, riskLevel, identityID, matchLevel, requirement, testInstant)
	return err
}

// seedRequestChain 准备 capability_requests 的两个外键目标以及一条请求本身。
func seedRequestChain(t *testing.T, db *DB) {
	t.Helper()

	ctx := t.Context()
	seedDeviceAndWorkspace(t, db)
	if err := insertAgent(ctx, db, agentValues(testAgentID, "hash-session-key")); err != nil {
		t.Fatalf("写入 Agent 失败：%v", err)
	}
	if err := insertRequest(ctx, db, testRequestID, "deciding", `{"repo":"Runcoor/opendelo"}`, nil); err != nil {
		t.Fatalf("写入能力请求失败：%v", err)
	}
}

// queryPlan 返回一条查询的 EXPLAIN QUERY PLAN 全文，用于断言索引确实被用上
func queryPlan(t *testing.T, db *DB, statement string, arguments ...any) string {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, arguments...) //nolint:gosec // statement 来自本文件的常量
	if err != nil {
		t.Fatalf("取查询计划失败：%v", err)
	}
	defer closeRows(t, rows)

	var lines []string
	for rows.Next() {
		var (
			id, parent, notUsed int
			detail              string
		)
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("解析查询计划失败：%v", err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历查询计划失败：%v", err)
	}
	return strings.Join(lines, " | ")
}

func TestServiceAdapters_HasNoColumnThatCouldHoldACredential(t *testing.T) {
	// Adapter 声明里记的是「注入到哪里」，不是「注入什么」。
	// 与 credential_references 用同一张脱敏词表把关（REQ-CRED-001 AC1 的同一条边界）。
	db := migratedDB(t)

	for _, column := range columnNames(t, db, "service_adapters") {
		normalized := strings.ReplaceAll(strings.ToLower(column), "-", "_")
		for _, word := range secretColumnWords {
			if strings.Contains(normalized, word) {
				t.Errorf("service_adapters.%s 的名字命中 %q，凭据不得写进 Adapter 声明", column, word)
			}
		}
	}
}

func TestServiceAdapters_KindIsLimitedToImplementedAdapters(t *testing.T) {
	// REQ-ADAPTER-006 AC1：注册表只含本期实现的四种。
	ctx := t.Context()
	db := migratedDB(t)

	for _, kind := range []string{"github", "cloudflare", "model", "generic-http"} {
		if err := insertAdapter(ctx, db, "01K1A"+kind, kind, kind, "low", "[]"); err != nil {
			t.Errorf("Adapter 种类 %q 被拒绝：%v", kind, err)
		}
	}
	for _, kind := range []string{"vercel", "resend", "slack"} {
		if err := insertAdapter(ctx, db, "01K1X"+kind, kind, kind, "low", "[]"); err == nil {
			t.Errorf("本期不实现的 Adapter 种类 %q 被接受了", kind)
		}
	}
}

func TestServiceAdapters_MissingRiskLevel_IsRejected(t *testing.T) {
	// REQ-ADAPTER-005 AC2：未声明 Risk Level 的配置无法保存。
	ctx := t.Context()
	db := migratedDB(t)

	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO service_adapters (
			id, service, kind, display_name, base_url, auth_scheme,
			capabilities, allowed_paths, allowed_methods, redaction_rules,
			default_risk_level, status, created_at, updated_at)
		 VALUES (?, 'internal-api', 'generic-http', '内部接口', 'https://api.example.com', 'header',
			'[]', '["/v1/*"]', '["GET"]', '[]', NULL, 'enabled', ?, ?)`,
		testAdapterID, testInstant, testInstant); err == nil {
		t.Error("没有声明 Risk Level 的 Adapter 配置被保存了")
	}

	for _, level := range []string{"low", "medium", "high"} {
		if err := insertAdapter(ctx, db, "01K1R"+level, "svc-"+level, "generic-http", level, "[]"); err != nil {
			t.Errorf("风险标签 %q 被拒绝：%v", level, err)
		}
	}
	if err := insertAdapter(ctx, db, "01K1RBAD", "svc-bad", "generic-http", "critical", "[]"); err == nil {
		t.Error("风险标签 \"critical\" 被接受了")
	}
}

func TestServiceAdapters_NonJSONDeclaration_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertAdapter(ctx, db, testAdapterID, "github", "github", "low", "not json"); err == nil {
		t.Error("非 JSON 的能力声明被接受了")
	}
}

func TestServiceAdapters_DuplicateService_IsRejected(t *testing.T) {
	// 两条同名声明会让「这个操作被允许吗」有两个答案。
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertAdapter(ctx, db, testAdapterID, "github", "github", "low", "[]"); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertAdapter(ctx, db, "01K1ADAPTER2", "github", "generic-http", "low", "[]"); err == nil {
		t.Error("同一服务名被登记了第二个 Adapter")
	}
	if err := insertAdapter(ctx, db, "01K1ADAPTER3", "cloudflare", "cloudflare", "low", "[]"); err != nil {
		t.Errorf("另一个服务名被拒绝：%v", err)
	}

	assertIndexIsUnique(t, db, "service_adapters", "uq_service_adapters_service")
}

func TestCapabilityRequests_EveryStatusInTheStateMachine_IsAccepted(t *testing.T) {
	// REQ-CAP-001 的状态机取值必须全部可存。漏掉一个，链路走到那一步就写不进去。
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	for index, status := range requestStatuses {
		id := testRequestID[:20] + string(rune('A'+index)) + "00000"
		if err := insertRequest(ctx, db, id, status, "{}", nil); err != nil {
			t.Errorf("状态 %q 被拒绝：%v", status, err)
		}
	}
}

func TestCapabilityRequests_StatusOutsideTheStateMachine_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	// pending / running / done 是常见的通用状态名，恰恰不属于这个状态机；
	// executed 与 auto_allow 则是与真实取值仅一字之差的写法。
	for _, status := range []string{"pending", "running", "done", "executed", "auto_allow", ""} {
		if err := insertRequest(ctx, db, "01K1BAD"+status, status, "{}", nil); err == nil {
			t.Errorf("状态机外的取值 %q 被接受了", status)
		}
	}
}

func TestCapabilityRequests_ReadOperation_HasNullDesiredChange(t *testing.T) {
	// 读操作没有期望变更。空对象会在审批页面上被读成「变更为空」，那是另一句话。
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	if err := insertRequest(ctx, db, "01K1READONLY0000000000000", "received", "{}", nil); err != nil {
		t.Fatalf("读操作请求被拒绝：%v", err)
	}
	if err := insertRequest(ctx, db, "01K1BADCHANGE000000000000", "received", "{}", "not json"); err == nil {
		t.Error("非 JSON 的 desired_change 被接受了")
	}
	if err := insertRequest(ctx, db, "01K1BADRESOURCE0000000000", "received", "not json", nil); err == nil {
		t.Error("非 JSON 的 resource 被接受了")
	}
}

func TestCapabilityRequests_UnknownAgentOrWorkspace_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO capability_requests (
			id, operation_id, agent_id, workspace_id, service, operation,
			resource, desired_change, reason, status, created_at, updated_at)
		 VALUES (?, '01K1OP', '01K1MISSING00000000000000', ?, 'github', 'repo.read',
			'{}', NULL, '理由', 'received', ?, ?)`,
		"01K1NOAGENT00000000000000", testWorkspaceID, testInstant, testInstant); err == nil {
		t.Error("指向不存在 Agent 的请求被插入了")
	}

	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM agents WHERE id = ?`, testAgentID); err == nil {
		t.Error("仍被请求引用的 Agent 被删除了")
	}
}

func TestCapabilityRequests_PendingListQuery_UsesTheIndex(t *testing.T) {
	// 待审批列表按状态过滤后按到达时间排序。索引必须同时满足两者，
	// 否则 SQLite 会为 ORDER BY 建临时 B 树。
	db := migratedDB(t)

	plan := queryPlan(t, db,
		`SELECT id FROM capability_requests WHERE status = ? ORDER BY created_at LIMIT ?`,
		"awaiting_approval", 50)

	if !strings.Contains(plan, "idx_capability_requests_status_created_at") {
		t.Errorf("待审批列表查询未命中索引，计划为：%s", plan)
	}
	if strings.Contains(plan, "SCAN") {
		t.Errorf("待审批列表查询出现了全表扫描，计划为：%s", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
		t.Errorf("待审批列表查询为排序建了临时 B 树，计划为：%s", plan)
	}
}

func TestDecisions_VerdictAndRiskLevel_AreLimitedToTheirSets(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name      string
		verdict   string
		riskLevel string
		accepted  bool
	}{
		{name: "自动放行", verdict: "auto_allow", riskLevel: "low", accepted: true},
		{name: "需要确认", verdict: "require_approval", riskLevel: "medium", accepted: true},
		{name: "拒绝", verdict: "deny", riskLevel: "high", accepted: true},
		{name: "第四种结论意味着第二条放行路径", verdict: "allow_once", riskLevel: "low", accepted: false},
		{name: "风险等级只有三级", verdict: "deny", riskLevel: "critical", accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedRequestChain(t, db)
			seedCredentialChain(t, db)
			if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
				t.Fatalf("写入身份失败：%v", err)
			}

			err := insertDecision(ctx, db, testDecisionID, testCase.verdict, testCase.riskLevel,
				"standard", testIdentityID, "workspace_binding")
			if testCase.accepted && err != nil {
				t.Errorf("合法取值被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("非法取值被接受了")
			}
		})
	}
}

func TestDecisions_IdentityAndMatchLevel_MustAppearTogether(t *testing.T) {
	// 匹配到身份却说不出命中哪一层，或者说得出层级却没有身份，
	// 都意味着这条决策记录本身是坏的（REQ-IDENT-002 AC3）。
	ctx := t.Context()

	cases := []struct {
		name       string
		identityID any
		matchLevel any
		accepted   bool
	}{
		{name: "身份与层级都有", identityID: testIdentityID, matchLevel: "workspace_binding", accepted: true},
		{name: "身份与层级都没有", identityID: nil, matchLevel: nil, accepted: true},
		{name: "有身份但没有层级", identityID: testIdentityID, matchLevel: nil, accepted: false},
		{name: "有层级但没有身份", identityID: nil, matchLevel: "sole_identity", accepted: false},
		{name: "层级不在五级之内", identityID: testIdentityID, matchLevel: "guessed", accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedRequestChain(t, db)
			seedCredentialChain(t, db)
			if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
				t.Fatalf("写入身份失败：%v", err)
			}

			err := insertDecision(ctx, db, testDecisionID, "require_approval", "medium",
				"standard", testCase.identityID, testCase.matchLevel)
			if testCase.accepted && err != nil {
				t.Errorf("合法组合被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("不一致的组合被接受了")
			}
		})
	}
}

func TestDecisions_EveryMatchLevel_IsAccepted(t *testing.T) {
	// 五级匹配顺序（REQ-IDENT-002）每一级都要能被记下来。
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)
	seedCredentialChain(t, db)
	if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}

	levels := []string{
		"workspace_binding", "resource_binding", "trust_memory",
		"sole_identity", "manual_selection",
	}
	for index, level := range levels {
		requestID := "01K1MATCHREQ" + string(rune('A'+index)) + "0000000000"
		if err := insertRequest(ctx, db, requestID, "deciding", "{}", nil); err != nil {
			t.Fatalf("写入能力请求失败：%v", err)
		}
		if _, err := db.Writer().ExecContext(ctx,
			`INSERT INTO decisions (
				id, capability_request_id, verdict, risk_level, risk_factors,
				identity_id, match_level, resolved_scope, approval_requirement,
				reason_code, created_at)
			 VALUES (?, ?, 'auto_allow', 'low', '[]', ?, ?, '{}', 'none', 'memory_hit', ?)`,
			"01K1MATCHDEC"+string(rune('A'+index))+"0000000000", requestID,
			testIdentityID, level, testInstant); err != nil {
			t.Errorf("匹配层级 %q 被拒绝：%v", level, err)
		}
	}
}

func TestDecisions_ApprovalRequirement_IsLimitedToItsSet(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	for index, requirement := range []string{"none", "standard", "strong_auth"} {
		requestID := "01K1REQREQ" + string(rune('A'+index)) + "000000000000"
		if err := insertRequest(ctx, db, requestID, "deciding", "{}", nil); err != nil {
			t.Fatalf("写入能力请求失败：%v", err)
		}
		if _, err := db.Writer().ExecContext(ctx,
			`INSERT INTO decisions (
				id, capability_request_id, verdict, risk_level, risk_factors,
				identity_id, match_level, resolved_scope, approval_requirement,
				reason_code, created_at)
			 VALUES (?, ?, 'deny', 'high', '[]', NULL, NULL, '{}', ?, 'no_identity', ?)`,
			"01K1DECREQ"+string(rune('A'+index))+"000000000000", requestID,
			requirement, testInstant); err != nil {
			t.Errorf("确认强度 %q 被拒绝：%v", requirement, err)
		}
	}

	if err := insertDecision(ctx, db, testDecisionID, "deny", "high", "biometric", nil, nil); err == nil {
		t.Error("确认强度 \"biometric\" 被接受了")
	}
}

func TestDecisions_SecondDecisionForTheSameRequest_IsRejected(t *testing.T) {
	// REQ-API-004 的幂等在存储层的保证：一个请求只有一个结论。
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)

	if err := insertDecision(ctx, db, testDecisionID, "deny", "high", "standard", nil, nil); err != nil {
		t.Fatalf("首次写入决策失败：%v", err)
	}
	if err := insertDecision(ctx, db, "01K1DECISION2", "auto_allow", "low", "none", nil, nil); err == nil {
		t.Error("同一请求被写入了第二个决策")
	}

	assertIndexIsUnique(t, db, "decisions", "uq_decisions_capability_request_id")
}

func TestDecisions_DeletingReferencedRequestOrIdentity_IsRestricted(t *testing.T) {
	// 删掉请求或身份会让账本里的决策失去出处（§4.2 RESTRICT）。
	ctx := t.Context()
	db := migratedDB(t)
	seedRequestChain(t, db)
	seedCredentialChain(t, db)
	if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if err := insertDecision(ctx, db, testDecisionID, "auto_allow", "low", "none",
		testIdentityID, "workspace_binding"); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}

	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM capability_requests WHERE id = ?`, testRequestID); err == nil {
		t.Error("仍被决策引用的能力请求被删除了")
	}
	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM identities WHERE id = ?`, testIdentityID); err == nil {
		t.Error("仍被决策引用的身份被删除了")
	}
}
