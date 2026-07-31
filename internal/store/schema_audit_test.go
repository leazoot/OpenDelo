package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * audit_events 表的约束检查。
 */

const testEventID = "01K1EVENTAAAAAAAAAAAAAAAAA"

// auditEventTypes 是 REQ-AUDIT-002 的十类事件，加上需求点名的三个
// security / 清理事件。前端过滤器与这份清单一一对应（AC2）。
var auditEventTypes = []string{
	"decision.auto_allowed", "decision.user_allowed", "decision.denied",
	"lease.created", "lease.expired", "lease.revoked",
	"adapter.executed", "error", "identity.matched", "risk.changed",
	"security.scope_injection_ignored", "security.secret_request_blocked",
	"audit.pruned",
}

// auditBodyWords 是「这一列可能装着请求或响应正文」的信号词。
// PRD §22.1 默认不记录正文与文件内容，所以列名里不该出现它们。
var auditBodyWords = []string{
	"body", "payload", "request_body", "response_body", "content", "raw",
}

func insertEvent(
	ctx context.Context, db *DB,
	id, eventType, outcome string, agentID, responseStatus, metadata, createdAt any,
) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO audit_events (
			id, operation_id, event_type, agent_id, device_id, workspace_id,
			identity_id, credential_provider_id, service, operation, resource,
			resolved_scope, verdict, risk_level, decision_id, approval_id,
			lease_id, lease_status, outcome, response_status, duration_ms,
			is_redacted, metadata, created_at)
		 VALUES (?, '01K1OP', ?, ?, NULL, NULL,
			NULL, NULL, 'github', 'repo.read', '{}',
			'{}', NULL, NULL, NULL, NULL,
			NULL, NULL, ?, ?, 120,
			1, ?, ?)`,
		id, eventType, agentID, outcome, responseStatus, metadata, createdAt)
	return err
}

func TestAuditEvents_EveryDeclaredEventType_IsAccepted(t *testing.T) {
	// REQ-AUDIT-002 AC1/AC2：十类事件加三个点名事件都要能写入，且枚举封闭。
	ctx := t.Context()
	db := migratedDB(t)

	for index, eventType := range auditEventTypes {
		id := "01K1EVT" + string(rune('A'+index)) + "0000000000000000"
		if err := insertEvent(ctx, db, id, eventType, "succeeded", nil, nil, "{}", testInstant); err != nil {
			t.Errorf("事件类型 %q 被拒绝：%v", eventType, err)
		}
	}

	// 通用名字恰恰不属于这份清单：写进来就意味着前端过滤器认不出它。
	for _, eventType := range []string{"info", "warning", "decision", "lease", ""} {
		if err := insertEvent(ctx, db, "01K1BAD"+eventType, eventType,
			"succeeded", nil, nil, "{}", testInstant); err == nil {
			t.Errorf("清单外的事件类型 %q 被接受了", eventType)
		}
	}
}

func TestAuditEvents_HasNoColumnForRequestOrResponseBodies(t *testing.T) {
	// PRD §22.1：默认不记录完整请求正文、完整响应正文、Secret、文件内容。
	// 这张表里不该有能装下它们的列 —— 与凭据表用同一种把关方式。
	db := migratedDB(t)

	for _, column := range columnNames(t, db, "audit_events") {
		normalized := strings.ReplaceAll(strings.ToLower(column), "-", "_")
		for _, word := range append(append([]string{}, secretColumnWords...), auditBodyWords...) {
			// credential_provider_id 记的是来源标识而不是凭据，需要放行。
			if column == "credential_provider_id" {
				continue
			}
			if strings.Contains(normalized, word) {
				t.Errorf("audit_events.%s 的名字命中 %q，正文与凭据都不入账本", column, word)
			}
		}
	}
}

func TestAuditEvents_UnidentifiedRequest_StillRecords(t *testing.T) {
	// 认不出 Agent 正是要拒绝并记录的情况之一。
	// 那时这些字段没有答案，必须允许留空 —— 否则这条记录根本写不进去，
	// 而「写不进去」意味着一条未审计的路径。
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertEvent(ctx, db, testEventID, "decision.denied",
		"blocked", nil, nil, "{}", testInstant); err != nil {
		t.Errorf("认不出请求者的事件被拒绝：%v", err)
	}
}

func TestAuditEvents_RequiredColumns_RejectNull(t *testing.T) {
	// 无论认不认得出请求者，这几列都必须有值：没有它们的记录解释不了任何事。
	ctx := t.Context()

	required := []string{
		"operation_id", "event_type", "service", "operation",
		"resource", "resolved_scope", "outcome", "duration_ms",
		"is_redacted", "metadata", "created_at",
	}
	notNull := notNullColumns(t, migratedDB(t), "audit_events")
	for _, column := range required {
		if !notNull[column] {
			t.Errorf("audit_events.%s 可以为空，这样的记录解释不了任何事", column)
		}
	}

	// 再用一次真实写入证明约束确实拦得住，而不只是 pragma 说它非空。
	db := migratedDB(t)
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO audit_events (
			id, operation_id, event_type, service, operation, resource,
			resolved_scope, outcome, duration_ms, is_redacted, metadata, created_at)
		 VALUES (?, NULL, 'error', 'github', 'repo.read', '{}', '{}', 'blocked', 0, 1, '{}', ?)`,
		testEventID, testInstant); err == nil {
		t.Error("没有 operation_id 的事件被写入了")
	}
}

func TestAuditEvents_OutcomeAndResponseStatus_AreValidated(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name           string
		outcome        string
		responseStatus any
		accepted       bool
	}{
		{name: "执行成功", outcome: "succeeded", responseStatus: 200, accepted: true},
		{name: "执行失败", outcome: "failed", responseStatus: 500, accepted: true},
		{name: "没有执行", outcome: "blocked", responseStatus: nil, accepted: true},
		{name: "结果取值非法", outcome: "unknown", responseStatus: nil, accepted: false},
		{name: "状态码不在 HTTP 范围内", outcome: "succeeded", responseStatus: 42, accepted: false},
		{name: "状态码为零而不是 NULL", outcome: "blocked", responseStatus: 0, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)

			err := insertEvent(ctx, db, testEventID, "adapter.executed",
				testCase.outcome, nil, testCase.responseStatus, "{}", testInstant)
			if testCase.accepted && err != nil {
				t.Errorf("合法组合被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("非法组合被接受了")
			}
		})
	}
}

func TestAuditEvents_NonJSONMetadata_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertEvent(ctx, db, testEventID, "error", "blocked", nil, nil, "not json", testInstant); err == nil {
		t.Error("非 JSON 的元数据被接受了")
	}
}

func TestAuditEvents_ForeignKeys_AreAllRestrict(t *testing.T) {
	// §4.2：审计相关外键一律 RESTRICT，审计不可因级联而丢失。
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)

	assertDeleteRuleIsRestrict(t, db, "audit_events")

	if err := insertEvent(ctx, db, testEventID, "identity.matched",
		"succeeded", testAgentID, nil, "{}", testInstant); err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, testAgentID); err == nil {
		t.Error("仍被审计事件引用的 Agent 被删除了")
	}
	// 指向不存在的 Agent 同样写不进去：账本里的引用必须是真的。
	if err := insertEvent(ctx, db, "01K1EVTGHOST", "identity.matched",
		"succeeded", "01K1MISSING00000000000000", nil, "{}", testInstant); err == nil {
		t.Error("指向不存在 Agent 的事件被写入了")
	}
}

func TestAuditEvents_LedgerQueries_UseTheIndexes(t *testing.T) {
	// REQ-AUDIT-003 的默认视图与两个主过滤维度（REQ-NFR-001：P95 < 300ms）。
	db := migratedDB(t)

	cases := []struct {
		name  string
		query string
		index string
		args  []any
	}{
		{
			name:  "按时间倒序翻页",
			query: `SELECT id FROM audit_events WHERE created_at < ? ORDER BY created_at DESC LIMIT ?`,
			index: "idx_audit_events_created_at",
			args:  []any{testLaterTime, 50},
		},
		{
			name:  "按 Agent 过滤",
			query: `SELECT id FROM audit_events WHERE agent_id = ? AND created_at < ? ORDER BY created_at DESC LIMIT ?`,
			index: "idx_audit_events_agent_id_created_at",
			args:  []any{testAgentID, testLaterTime, 50},
		},
		{
			name:  "按服务过滤",
			query: `SELECT id FROM audit_events WHERE service = ? AND created_at < ? ORDER BY created_at DESC LIMIT ?`,
			index: "idx_audit_events_service_created_at",
			args:  []any{"github", testLaterTime, 50},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan := queryPlan(t, db, testCase.query, testCase.args...)
			if !strings.Contains(plan, testCase.index) {
				t.Errorf("未命中 %s，计划为：%s", testCase.index, plan)
			}
			if strings.Contains(plan, "SCAN") {
				t.Errorf("出现了全表扫描，计划为：%s", plan)
			}
			if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
				t.Errorf("为排序建了临时 B 树，计划为：%s", plan)
			}
		})
	}
}
