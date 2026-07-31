package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * approvals / leases 两张表的约束检查。
 */

const (
	testApprovalID = "01K1APPROVALAAAAAAAAAAAAAA"
	testLeaseID    = "01K1LEASEAAAAAAAAAAAAAAAAA"
	testLaterTime  = "2026-07-28T09:30:30.123Z"
)

func insertApproval(ctx context.Context, db *DB, id, status string, action, decidedAt any) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO approvals (
			id, decision_id, action, status, expires_at, decided_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, testDecisionID, action, status, testLaterTime, decidedAt, testInstant, testInstant)
	return err
}

func insertLease(
	ctx context.Context, db *DB,
	id, status string, expiresAt, requestLimit any, usedRequests int, approvalID any, scope string,
) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO leases (
			id, agent_id, identity_id, service, resource_scope, capabilities,
			expires_at, request_limit, used_requests, status, approval_id,
			is_session_bound, created_at, updated_at)
		 VALUES (?, ?, ?, 'github', ?, '["pull_request.create"]',
			?, ?, ?, ?, ?, 0, ?, ?)`,
		id, testAgentID, testIdentityID, scope,
		expiresAt, requestLimit, usedRequests, status, approvalID, testInstant, testInstant)
	return err
}

// seedDecisionChain 把请求链、凭据链、身份与一条决策全部准备好，
// 使审批项与 Lease 的外键都有目标。
func seedDecisionChain(t *testing.T, db *DB) {
	t.Helper()

	ctx := t.Context()
	seedRequestChain(t, db)
	seedCredentialChain(t, db)
	if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	// 决策刻意不带身份：identities 因此只被 Lease 引用，
	// 下面的删除用例才测得到 leases 自己的外键行为，而不是被 decisions 的外键挡下来。
	if err := insertDecision(ctx, db, testDecisionID, "require_approval", "medium",
		"standard", nil, nil); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
}

func TestApprovals_EveryActionAndStatus_IsAccepted(t *testing.T) {
	// REQ-APPROVAL-002 的五种操作与五个生命周期状态都要能存下来。
	ctx := t.Context()
	db := migratedDB(t)
	seedDecisionChain(t, db)

	actions := []string{
		"deny", "allow_once", "allow_until_task_end",
		"auto_allow_in_project", "always_ask",
	}
	for index, action := range actions {
		id := "01K1APPROVALACT" + string(rune('A'+index)) + "0000000000"
		if err := insertDecisionFor(ctx, db, index); err != nil {
			t.Fatalf("准备决策失败：%v", err)
		}
		if _, err := db.Writer().ExecContext(ctx,
			`INSERT INTO approvals (
				id, decision_id, action, status, expires_at, decided_at, created_at, updated_at)
			 VALUES (?, ?, ?, 'approved', ?, ?, ?, ?)`,
			id, decisionIDFor(index), action, testLaterTime, testInstant, testInstant, testInstant,
		); err != nil {
			t.Errorf("审批操作 %q 被拒绝：%v", action, err)
		}
	}

	if err := insertApproval(ctx, db, testApprovalID, "pending", nil, nil); err != nil {
		t.Errorf("待决审批项被拒绝：%v", err)
	}
	if err := insertApproval(ctx, db, "01K1APPROVALBAD", "settled", nil, nil); err == nil {
		t.Error("状态 \"settled\" 被接受了")
	}
}

func TestApprovals_ActionOutsideTheFiveOperations_IsRejected(t *testing.T) {
	// allow_forever 与 auto_allow 都是「看起来很像」的写法，存进来之后
	// 会在放行判断里被当成某种允许。
	//
	// 每个取值一份独立的数据库：审批项与决策是一对一的，共用一个决策会让
	// 插入因为唯一索引而失败，用例就变成了在测唯一索引。
	ctx := t.Context()

	for _, action := range []string{"allow_forever", "auto_allow", "approve", ""} {
		t.Run("操作取值 "+action, func(t *testing.T) {
			db := migratedDB(t)
			seedDecisionChain(t, db)

			if err := insertApproval(ctx, db, testApprovalID, "approved", action, testInstant); err == nil {
				t.Errorf("五种操作之外的取值 %q 被接受了", action)
			}
		})
	}
}

func TestApprovals_PendingMustNotCarryAResult(t *testing.T) {
	// 只写了一半的审批项会让「这次是谁放行的」答不出来。
	ctx := t.Context()

	cases := []struct {
		name      string
		status    string
		action    any
		decidedAt any
		accepted  bool
	}{
		{name: "待决且无结果", status: "pending", action: nil, decidedAt: nil, accepted: true},
		{name: "已决且有完整结果", status: "approved", action: "allow_once", decidedAt: testInstant, accepted: true},
		{name: "待决却带着操作", status: "pending", action: "allow_once", decidedAt: nil, accepted: false},
		{name: "待决却带着决出时刻", status: "pending", action: nil, decidedAt: testInstant, accepted: false},
		{name: "已决却没有操作", status: "approved", action: nil, decidedAt: testInstant, accepted: false},
		{name: "已决却没有决出时刻", status: "rejected", action: "deny", decidedAt: nil, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedDecisionChain(t, db)

			err := insertApproval(ctx, db, testApprovalID, testCase.status, testCase.action, testCase.decidedAt)
			if testCase.accepted && err != nil {
				t.Errorf("合法组合被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("不一致的组合被接受了")
			}
		})
	}
}

func TestApprovals_MissingExpiresAt_IsRejected(t *testing.T) {
	// 一个永远等下去的审批项等于一条永久授权的入口。
	ctx := t.Context()
	db := migratedDB(t)
	seedDecisionChain(t, db)

	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO approvals (
			id, decision_id, action, status, expires_at, decided_at, created_at, updated_at)
		 VALUES (?, ?, NULL, 'pending', NULL, NULL, ?, ?)`,
		testApprovalID, testDecisionID, testInstant, testInstant); err == nil {
		t.Error("没有超时时刻的审批项被插入了")
	}
}

func TestApprovals_SecondApprovalForTheSameDecision_IsRejected(t *testing.T) {
	// 同一次请求不能出现两个可以分别放行的入口。
	ctx := t.Context()
	db := migratedDB(t)
	seedDecisionChain(t, db)

	if err := insertApproval(ctx, db, testApprovalID, "pending", nil, nil); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertApproval(ctx, db, "01K1APPROVAL2", "pending", nil, nil); err == nil {
		t.Error("同一个决策被创建了第二个审批项")
	}

	assertIndexIsUnique(t, db, "approvals", "uq_approvals_decision_id")
}

func TestApprovals_DeletingReferencedDecision_IsRestricted(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedDecisionChain(t, db)
	if err := insertApproval(ctx, db, testApprovalID, "pending", nil, nil); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}

	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM decisions WHERE id = ?`, testDecisionID); err == nil {
		t.Error("仍被审批项引用的决策被删除了")
	}
}

func TestApprovals_TimeoutSweepQuery_UsesTheIndex(t *testing.T) {
	db := migratedDB(t)

	plan := queryPlan(t, db,
		`SELECT id FROM approvals WHERE status = 'pending' AND expires_at <= ? ORDER BY expires_at LIMIT ?`,
		testLaterTime, 50)

	if !strings.Contains(plan, "idx_approvals_status_expires_at") {
		t.Errorf("超时清扫查询未命中索引，计划为：%s", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
		t.Errorf("超时清扫查询为排序建了临时 B 树，计划为：%s", plan)
	}
}

func TestLeases_MissingExpiresAt_IsRejected(t *testing.T) {
	// REQ-LEASE-001 AC1：不存在 expires_at 为空的 Lease。
	// 「永久授权」在 schema 层面就不可表达，这条约束不依赖任何应用层代码。
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)

	if err := insertLease(ctx, db, testLeaseID, "active", nil, 3, 0, testApprovalID, "{}"); err == nil {
		t.Error("没有到期时刻的 Lease 被插入了")
	}
	if err := insertLease(ctx, db, testLeaseID, "active", testLaterTime, 3, 0, testApprovalID, "{}"); err != nil {
		t.Errorf("有到期时刻的 Lease 被拒绝：%v", err)
	}
}

func TestLeases_UsedRequestsCanNeverExceedTheLimit(t *testing.T) {
	// 计数递增用条件更新防并发，这条 CHECK 是它的后盾：
	// 任何绕过条件更新的写入路径也超发不了。
	ctx := t.Context()

	cases := []struct {
		name         string
		requestLimit any
		usedRequests int
		accepted     bool
	}{
		{name: "尚未使用", requestLimit: 3, usedRequests: 0, accepted: true},
		{name: "刚好用满", requestLimit: 3, usedRequests: 3, accepted: true},
		{name: "超出上限", requestLimit: 3, usedRequests: 4, accepted: false},
		{name: "不限次数时任意计数", requestLimit: nil, usedRequests: 99, accepted: true},
		{name: "负数计数", requestLimit: 3, usedRequests: -1, accepted: false},
		{name: "上限为零", requestLimit: 0, usedRequests: 0, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedLeaseChain(t, db)

			err := insertLease(ctx, db, testLeaseID, "active", testLaterTime,
				testCase.requestLimit, testCase.usedRequests, testApprovalID, "{}")
			if testCase.accepted && err != nil {
				t.Errorf("合法组合被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("超发的组合被接受了")
			}
		})
	}
}

func TestLeases_SecondLeaseForTheSameApproval_IsRejected(t *testing.T) {
	// 数据库规则点名的唯一索引。
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)

	if err := insertLease(ctx, db, testLeaseID, "active", testLaterTime, 3, 0, testApprovalID, "{}"); err != nil {
		t.Fatalf("首次签发失败：%v", err)
	}
	if err := insertLease(ctx, db, "01K1LEASE2", "active", testLaterTime, 3, 0, testApprovalID, "{}"); err == nil {
		t.Error("同一个审批项签发了第二条 Lease")
	}

	assertIndexIsUnique(t, db, "leases", "uq_leases_approval_id")
}

func TestLeases_AutoAllowedLeases_ShareTheNullApproval(t *testing.T) {
	// 自动放行的 Lease 没有审批项。SQLite 的唯一索引把多个 NULL 视为互不相同，
	// 所以它们不会因为「都没有 approval_id」而互相排斥。
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)

	if err := insertLease(ctx, db, testLeaseID, "active", testLaterTime, 3, 0, nil, "{}"); err != nil {
		t.Fatalf("签发自动放行的 Lease 失败：%v", err)
	}
	if err := insertLease(ctx, db, "01K1LEASEAUTO2", "active", testLaterTime, 3, 0, nil, "{}"); err != nil {
		t.Errorf("第二条自动放行的 Lease 被拒绝：%v", err)
	}
}

func TestLeases_StatusAndScope_AreValidated(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)

	for index, status := range []string{"active", "expired", "exhausted", "revoked"} {
		id := "01K1LEASEST" + string(rune('A'+index)) + "00000000000000"
		if err := insertLease(ctx, db, id, status, testLaterTime, 3, 0, nil, "{}"); err != nil {
			t.Errorf("状态 %q 被拒绝：%v", status, err)
		}
	}
	if err := insertLease(ctx, db, "01K1LEASEBAD", "paused", testLaterTime, 3, 0, nil, "{}"); err == nil {
		t.Error("状态 \"paused\" 被接受了")
	}
	if err := insertLease(ctx, db, "01K1LEASEBADSCOPE", "active", testLaterTime, 3, 0, nil, "not json"); err == nil {
		t.Error("非 JSON 的资源范围被接受了")
	}
}

func TestLeases_DeletingReferencedRows_IsRestricted(t *testing.T) {
	// 删掉 Agent、身份或审批项会让账本里的 Lease 失去出处。
	ctx := t.Context()
	db := migratedDB(t)
	seedLeaseChain(t, db)
	if err := insertLease(ctx, db, testLeaseID, "active", testLaterTime, 3, 0, testApprovalID, "{}"); err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	// 逐条断言外键的删除行为。只做删除实验是不够的：agents 同时被
	// capability_requests 引用，即使 leases 这条外键被改成 CASCADE，删除也仍会失败，
	// 用例会因为另一条外键而「通过」。
	assertDeleteRuleIsRestrict(t, db, "leases")
	assertDeleteRuleIsRestrict(t, db, "approvals")

	for _, deletion := range []struct {
		name      string
		statement string
		id        string
	}{
		{name: "Agent", statement: `DELETE FROM agents WHERE id = ?`, id: testAgentID},
		{name: "身份", statement: `DELETE FROM identities WHERE id = ?`, id: testIdentityID},
		{name: "审批项", statement: `DELETE FROM approvals WHERE id = ?`, id: testApprovalID},
	} {
		if _, err := db.Writer().ExecContext(ctx, deletion.statement, deletion.id); err == nil { //nolint:gosec // 语句来自本文件的常量
			t.Errorf("仍被 Lease 引用的%s被删除了", deletion.name)
		}
	}
}

// assertDeleteRuleIsRestrict 断言一张表的每条外键都是 ON DELETE RESTRICT
// （审计不可因级联而丢失）。
func assertDeleteRuleIsRestrict(t *testing.T, db *DB, table string) {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT "table", "from", on_delete FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		t.Fatalf("读取 %s 的外键失败：%v", table, err)
	}
	defer closeRows(t, rows)

	var count int
	for rows.Next() {
		var target, column, onDelete string
		if err := rows.Scan(&target, &column, &onDelete); err != nil {
			t.Fatalf("解析外键失败：%v", err)
		}
		count++
		if onDelete != "RESTRICT" {
			t.Errorf("%s.%s → %s 的删除行为是 %q，期望 RESTRICT", table, column, target, onDelete)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历外键失败：%v", err)
	}
	if count == 0 {
		t.Errorf("%s 没有任何外键，这条断言退化成了永真", table)
	}
}

func TestLeases_ActiveListQuery_UsesTheIndex(t *testing.T) {
	// 到期扫描与 Gate 缝内侧的 Active leases 用的是同一个索引。
	db := migratedDB(t)

	plan := queryPlan(t, db,
		`SELECT id FROM leases WHERE status = 'active' AND expires_at <= ? ORDER BY expires_at LIMIT ?`,
		testLaterTime, 50)

	if !strings.Contains(plan, "idx_leases_status_expires_at") {
		t.Errorf("到期扫描查询未命中索引，计划为：%s", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
		t.Errorf("到期扫描查询为排序建了临时 B 树，计划为：%s", plan)
	}
}

// seedLeaseChain 在决策链之外再写入一个待决审批项，使 Lease 的外键都有目标。
func seedLeaseChain(t *testing.T, db *DB) {
	t.Helper()

	seedDecisionChain(t, db)
	if err := insertApproval(t.Context(), db, testApprovalID, "pending", nil, nil); err != nil {
		t.Fatalf("写入审批项失败：%v", err)
	}
}

// insertDecisionFor 为「每种审批操作各一条」的用例准备独立的请求与决策，
// 因为一个决策只能有一个审批项。
func insertDecisionFor(ctx context.Context, db *DB, index int) error {
	requestID := "01K1APPROVALREQ" + string(rune('A'+index)) + "0000000000"
	if err := insertRequest(ctx, db, requestID, "awaiting_approval", "{}", nil); err != nil {
		return err
	}
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO decisions (
			id, capability_request_id, verdict, risk_level, risk_factors,
			identity_id, match_level, resolved_scope, approval_requirement,
			reason_code, created_at)
		 VALUES (?, ?, 'require_approval', 'medium', '[]', NULL, NULL, '{}', 'standard', 'needs_review', ?)`,
		decisionIDFor(index), requestID, testInstant)
	return err
}

func decisionIDFor(index int) string {
	return "01K1APPROVALDEC" + string(rune('A'+index)) + "0000000000"
}
