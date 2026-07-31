package store

import (
	"testing"
)

/*
 * 迁移 00017 · 审计事件类型扩充（用户决定 D-07）。
 *
 * 这里测的不是「CHECK 放宽了」——那由 schema_audit_test 的枚举用例覆盖 ——
 * 而是**回滚时那两类记录怎么办**。用户在 A1（改写为 error）与 A2（删除）
 * 之间选了 A1，理由是审计行不因回滚而丢失。没有用例，那个选择就只是注释。
 */

// insertEventOfType 写入一条最简的审计记录，只有类型按用例变化。
func insertEventOfType(t *testing.T, db *DB, id, eventType string) {
	t.Helper()

	_, err := db.Writer().ExecContext(t.Context(), `
		INSERT INTO audit_events (
			id, operation_id, event_type, service, operation,
			resource, resolved_scope, outcome, duration_ms, is_redacted,
			metadata, created_at
		) VALUES (?, ?, ?, 'github', 'read_repository',
			'{}', '{}', 'blocked', 0, 1, '{}', '2026-07-29T00:00:00.000Z')`,
		id, "op_"+id, eventType)
	if err != nil {
		t.Fatalf("写入 %s 类型的审计记录失败：%v", eventType, err)
	}
}

func eventTypeOf(t *testing.T, db *DB, id string) string {
	t.Helper()

	var eventType string
	if err := db.Reader().QueryRowContext(t.Context(),
		`SELECT event_type FROM audit_events WHERE id = ?`, id).Scan(&eventType); err != nil {
		t.Fatalf("读取 %s 的事件类型失败：%v", id, err)
	}
	return eventType
}

func TestMigrate00017_Rollback_RewritesTheNewTypesInsteadOfDroppingTheRows(t *testing.T) {
	ctx := t.Context()
	db := openForMigration(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	// 三条记录：两条是新类型，一条是旧类型的对照组。
	insertEventOfType(t, db, "01K1AUDIT000000000000000001", "agent.identity_mismatch")
	insertEventOfType(t, db, "01K1AUDIT000000000000000002", "agent.trusted")
	insertEventOfType(t, db, "01K1AUDIT000000000000000003", "decision.denied")

	if err := rollback(ctx, db, 16); err != nil {
		t.Fatalf("回滚到 16 失败：%v", err)
	}

	var remaining int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events`).Scan(&remaining); err != nil {
		t.Fatalf("统计审计记录失败：%v", err)
	}
	if remaining != 3 {
		t.Fatalf("回滚后剩 %d 条审计记录，期望 3 条 —— 审计行不该因回滚而消失", remaining)
	}

	for _, id := range []string{"01K1AUDIT000000000000000001", "01K1AUDIT000000000000000002"} {
		if actual := eventTypeOf(t, db, id); actual != "error" {
			t.Errorf("%s 回滚后的类型是 %q，期望被改写为 error", id, actual)
		}
	}
	if actual := eventTypeOf(t, db, "01K1AUDIT000000000000000003"); actual != "decision.denied" {
		t.Errorf("对照组的类型被改成了 %q，回滚只该动新增的那两类", actual)
	}
}

func TestMigrate00017_AfterRollback_TheNewTypesAreRefusedAgain(t *testing.T) {
	// 收紧回去之后 CHECK 必须真的生效，否则「回滚」只是改了个注释。
	ctx := t.Context()
	db := openForMigration(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if err := rollback(ctx, db, 16); err != nil {
		t.Fatalf("回滚到 16 失败：%v", err)
	}

	for _, eventType := range []string{"agent.identity_mismatch", "agent.trusted"} {
		_, err := db.Writer().ExecContext(ctx, `
			INSERT INTO audit_events (
				id, operation_id, event_type, service, operation,
				resource, resolved_scope, outcome, duration_ms, is_redacted,
				metadata, created_at
			) VALUES ('01K1AUDIT000000000000000009', 'op_x', ?, 'github', 'read_repository',
				'{}', '{}', 'blocked', 0, 1, '{}', '2026-07-29T00:00:00.000Z')`, eventType)
		if err == nil {
			t.Errorf("回滚之后 %q 仍然写得进去，CHECK 没有被收紧", eventType)
		}
	}
}

func TestMigrate00017_TheTwoNewTypes_AreAcceptedAfterUp(t *testing.T) {
	// 正向：迁移之后两类记录写得进去。没有这条，上面两条用例可以靠
	// 「这两类根本写不进去」通过。
	ctx := t.Context()
	db := openForMigration(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	insertEventOfType(t, db, "01K1AUDIT000000000000000011", "agent.identity_mismatch")
	insertEventOfType(t, db, "01K1AUDIT000000000000000012", "agent.trusted")

	if actual := eventTypeOf(t, db, "01K1AUDIT000000000000000011"); actual != "agent.identity_mismatch" {
		t.Errorf("写进去的类型变成了 %q", actual)
	}
}
