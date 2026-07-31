package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * trust_memories 表与两列记忆引用的约束检查。
 */

const testMemoryID = "01K1MEMORYAAAAAAAAAAAAAAAA"

// memoryDimensions 是 REQ-TRUST-002 点名的七个维度在表里的列名。
// 任何一维可空都等于「这一维不限」，那正是扩大。
var memoryDimensions = []string{
	"resource_scope", "capability_scope", "expires_at",
	"agent_id", "workspace_id", "identity_id", "environment",
}

func insertMemory(
	ctx context.Context, db *DB,
	id, service, riskCeiling, behavior, status string, reason, lastUsedAt any, createdFrom string,
) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO trust_memories (
			id, agent_id, workspace_id, identity_id, service,
			resource_scope, capability_scope, environment, risk_ceiling,
			approval_behavior, created_from, status, invalidation_reason,
			last_used_at, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?,
			'{"repo":"Runcoor/opendelo"}', '["pull_request.create"]', 'production', ?,
			?, ?, ?, ?, ?, ?, ?, ?)`,
		id, testAgentID, testWorkspaceID, testIdentityID, service,
		riskCeiling, behavior, createdFrom, status, reason, lastUsedAt,
		testLaterTime, testInstant, testInstant)
	return err
}

// seedMemoryChain 把决策链与一个待决审批项准备好，使记忆的外键都有目标。
func seedMemoryChain(t *testing.T, db *DB) {
	t.Helper()

	seedLeaseChain(t, db)
}

func TestTrustMemories_EverySevenDimensions_AreRequired(t *testing.T) {
	// REQ-TRUST-002：七个维度都必须有值。
	// 逐列断言 NOT NULL —— 少一维就是把那一维放开成「不限」。
	db := migratedDB(t)

	notNull := notNullColumns(t, db, "trust_memories")
	for _, dimension := range memoryDimensions {
		if !notNull[dimension] {
			t.Errorf("trust_memories.%s 可以为空，等于把这一维放开成「不限」", dimension)
		}
	}
}

func TestTrustMemories_RiskCeilingNeverReachesHigh(t *testing.T) {
	// REQ-TRUST-003：高风险永远需要人工确认。
	// 「能自动放行高风险的记忆」在 schema 层面就不可表达。
	ctx := t.Context()
	db := migratedDB(t)
	seedMemoryChain(t, db)

	for _, ceiling := range []string{"low", "medium"} {
		if err := insertMemory(ctx, db, "01K1MEM"+ceiling, "github", ceiling,
			"auto_allow", "active", nil, nil, testApprovalID); err != nil {
			t.Errorf("风险上限 %q 被拒绝：%v", ceiling, err)
		}
		// 一次审批只学一条记忆，换下一个取值前先腾出 created_from。
		if _, err := db.Writer().ExecContext(ctx, `DELETE FROM trust_memories WHERE id = ?`,
			"01K1MEM"+ceiling); err != nil {
			t.Fatalf("清理记忆失败：%v", err)
		}
	}

	for _, ceiling := range []string{"high", "critical", ""} {
		if err := insertMemory(ctx, db, "01K1MEMX"+ceiling, "github", ceiling,
			"auto_allow", "active", nil, nil, testApprovalID); err == nil {
			t.Errorf("风险上限 %q 被接受了", ceiling)
		}
	}
}

func TestTrustMemories_BehaviorIsLimitedToItsSet(t *testing.T) {
	// 每个取值一份独立的数据库：created_from 是唯一的，共用一个审批项会让
	// 第二次插入因为唯一索引而失败，用例就变成了在测唯一索引。
	ctx := t.Context()

	cases := []struct {
		behavior string
		accepted bool
	}{
		{behavior: "auto_allow", accepted: true},
		{behavior: "always_ask", accepted: true},
		{behavior: "auto_allow_forever", accepted: false},
		{behavior: "ask", accepted: false},
		{behavior: "", accepted: false},
	}

	for _, testCase := range cases {
		t.Run("命中行为 "+testCase.behavior, func(t *testing.T) {
			db := migratedDB(t)
			seedMemoryChain(t, db)

			err := insertMemory(ctx, db, testMemoryID, "github", "low",
				testCase.behavior, "active", nil, nil, testApprovalID)
			if testCase.accepted && err != nil {
				t.Errorf("合法取值被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("两种行为之外的取值被接受了")
			}
		})
	}
}

func TestTrustMemories_StatusAndInvalidationReason_MustAppearTogether(t *testing.T) {
	// 失效的记忆必须说得出原因（REQ-TRUST-004 AC2），
	// 生效中的记忆不能带着原因。
	ctx := t.Context()

	cases := []struct {
		name     string
		status   string
		reason   any
		accepted bool
	}{
		{name: "生效中且无原因", status: "active", reason: nil, accepted: true},
		{name: "已失效且有原因", status: "invalidated", reason: "unused_too_long", accepted: true},
		{name: "生效中却带着原因", status: "active", reason: "device_untrusted", accepted: false},
		{name: "已失效却说不出原因", status: "invalidated", reason: nil, accepted: false},
		{name: "八个条件之外的原因", status: "invalidated", reason: "user_deleted", accepted: false},
		{name: "状态本身非法", status: "paused", reason: nil, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedMemoryChain(t, db)

			err := insertMemory(ctx, db, testMemoryID, "github", "low",
				"auto_allow", testCase.status, testCase.reason, nil, testApprovalID)
			if testCase.accepted && err != nil {
				t.Errorf("合法组合被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("不一致的组合被接受了")
			}
		})
	}
}

func TestTrustMemories_EveryInvalidationReason_IsAccepted(t *testing.T) {
	// REQ-TRUST-004 的八个条件都要能记下来。
	ctx := t.Context()
	db := migratedDB(t)
	seedMemoryChain(t, db)

	reasons := []string{
		"provider_disconnected", "identity_scope_changed", "agent_executable_changed",
		"project_fingerprint_changed", "device_untrusted", "unused_too_long",
		"cautious_mode_selected", "adapter_risk_upgraded",
	}
	for index, reason := range reasons {
		id := "01K1MEMREASON" + string(rune('A'+index)) + "000000000"
		if err := insertMemory(ctx, db, id, "github", "low",
			"auto_allow", "invalidated", reason, nil, testApprovalID); err != nil {
			t.Errorf("失效原因 %q 被拒绝：%v", reason, err)
		}
		if _, err := db.Writer().ExecContext(ctx, `DELETE FROM trust_memories WHERE id = ?`, id); err != nil {
			t.Fatalf("清理记忆失败：%v", err)
		}
	}
}

func TestTrustMemories_SecondMemoryFromTheSameApproval_IsRejected(t *testing.T) {
	// 同一次确认被学两遍，用户删掉一条也止不住另一条。
	ctx := t.Context()
	db := migratedDB(t)
	seedMemoryChain(t, db)

	if err := insertMemory(ctx, db, testMemoryID, "github", "low",
		"auto_allow", "active", nil, nil, testApprovalID); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertMemory(ctx, db, "01K1MEMORY2", "github", "low",
		"auto_allow", "active", nil, nil, testApprovalID); err == nil {
		t.Error("同一次审批学出了第二条记忆")
	}

	assertIndexIsUnique(t, db, "trust_memories", "uq_trust_memories_created_from")
}

func TestTrustMemories_ForeignKeys_AreAllRestrict(t *testing.T) {
	// 删掉 Agent、项目、身份或审批项会让记忆失去出处（§4.2 RESTRICT）。
	ctx := t.Context()
	db := migratedDB(t)
	seedMemoryChain(t, db)
	if err := insertMemory(ctx, db, testMemoryID, "github", "low",
		"auto_allow", "active", nil, nil, testApprovalID); err != nil {
		t.Fatalf("写入记忆失败：%v", err)
	}

	assertDeleteRuleIsRestrict(t, db, "trust_memories")

	// 审批项只被记忆引用，删除实验对它是干净的。
	if _, err := db.Writer().ExecContext(ctx, `DELETE FROM approvals WHERE id = ?`, testApprovalID); err == nil {
		t.Error("仍被记忆引用的审批项被删除了")
	}
}

func TestTrustMemories_MatchQuery_UsesTheIndex(t *testing.T) {
	// REQ-IDENT-002 的匹配查询（P95 < 10ms）必须走索引。
	db := migratedDB(t)

	plan := queryPlan(t, db,
		`SELECT id FROM trust_memories
		 WHERE agent_id = ? AND workspace_id = ? AND service = ? AND status = 'active'
		 ORDER BY id LIMIT ?`,
		testAgentID, testWorkspaceID, "github", 50)

	if !strings.Contains(plan, "idx_trust_memories_agent_workspace_service_id") {
		t.Errorf("记忆匹配查询未命中索引，计划为：%s", plan)
	}
	if strings.Contains(plan, "SCAN") {
		t.Errorf("记忆匹配查询出现了全表扫描，计划为：%s", plan)
	}
	// 尾列 id 让排序也走索引。少了它，同一三元组下记忆一多就要整集排序，
	// 实测 10 万条时 P95 从 1.1ms 掉到 43ms。
	if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
		t.Errorf("记忆匹配查询为排序建了临时 B 树，计划为：%s", plan)
	}
}

func TestMemoryReferences_AreNullableAndRestrictDeletion(t *testing.T) {
	// decisions.trust_memory_id 与 leases.source_memory_id 是 PRD §9.6 / §9.7
	// 点名的两列。未命中记忆时为空，命中后不允许把记忆删掉 —— 那会让账本里
	// 「这次是靠哪条记忆放行的」失去答案。
	ctx := t.Context()
	db := migratedDB(t)
	seedMemoryChain(t, db)
	if err := insertMemory(ctx, db, testMemoryID, "github", "low",
		"auto_allow", "active", nil, nil, testApprovalID); err != nil {
		t.Fatalf("写入记忆失败：%v", err)
	}

	// 两行都必须先存在，否则 UPDATE 影响零行，用例就变成了在测「什么都没发生」。
	if err := insertLease(ctx, db, testLeaseID, "active", testLaterTime, 3, 0, testApprovalID, "{}"); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	assertRowExists(t, db, "decisions", testDecisionID)
	assertRowExists(t, db, "leases", testLeaseID)

	for _, reference := range []struct {
		name      string
		statement string
		id        string
	}{
		{
			name:      "decisions.trust_memory_id",
			statement: `UPDATE decisions SET trust_memory_id = ? WHERE id = ?`,
			id:        testDecisionID,
		},
		{
			name:      "leases.source_memory_id",
			statement: `UPDATE leases SET source_memory_id = ? WHERE id = ?`,
			id:        testLeaseID,
		},
	} {
		if _, err := db.Writer().ExecContext(ctx, reference.statement, //nolint:gosec // 语句来自本文件的常量
			"01K1MISSING00000000000000", reference.id); err == nil {
			t.Errorf("%s 指向了不存在的记忆", reference.name)
		}
		if _, err := db.Writer().ExecContext(ctx, reference.statement, //nolint:gosec // 语句来自本文件的常量
			testMemoryID, reference.id); err != nil {
			t.Errorf("%s 指向存在的记忆时被拒绝：%v", reference.name, err)
		}
	}

	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM trust_memories WHERE id = ?`, testMemoryID); err == nil {
		t.Error("仍被决策与 Lease 引用的记忆被删除了")
	}
}

// assertRowExists 守住「用例正在操作一行真实存在的数据」这个前提。
// 缺了它，一条 UPDATE 影响零行也会被当成约束生效。
func assertRowExists(t *testing.T, db *DB, table, id string) {
	t.Helper()

	var found int
	//nolint:gosec // 表名来自本文件的常量
	if err := db.Reader().QueryRowContext(t.Context(),
		`SELECT count(*) FROM `+table+` WHERE id = ?`, id).Scan(&found); err != nil {
		t.Fatalf("检查 %s 中的 %s 失败：%v", table, id, err)
	}
	if found != 1 {
		t.Fatalf("%s 中没有 %s，用例的前提不成立", table, id)
	}
}

// notNullColumns 返回一张表上标了 NOT NULL 的列名集合。
func notNullColumns(t *testing.T, db *DB, table string) map[string]bool {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT name, "notnull" FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("读取 %s 的表结构失败：%v", table, err)
	}
	defer closeRows(t, rows)

	notNull := make(map[string]bool)
	for rows.Next() {
		var (
			name    string
			flagged int
		)
		if err := rows.Scan(&name, &flagged); err != nil {
			t.Fatalf("解析表结构失败：%v", err)
		}
		notNull[name] = flagged == 1
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败：%v", err)
	}
	if len(notNull) == 0 {
		t.Fatalf("%s 没有任何列，这条断言退化成了永真", table)
	}
	return notNull
}
