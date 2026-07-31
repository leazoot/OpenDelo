package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * devices / workspaces / agents 三张表的约束检查。
 *
 * 用例走原生 SQL 而不是仓储：NOT NULL 只能靠写入 NULL 来触发，而类型化的仓储
 * 签名根本表达不出 NULL。约束是最后一道防线，必须直接对着数据库验。
 */

const (
	testDeviceID    = "01K1DEVICEAAAAAAAAAAAAAAAA"
	testWorkspaceID = "01K1WORKSPACEAAAAAAAAAAAAA"
	testInstant     = "2026-07-28T09:15:30.123Z"
)

// agentColumns 固定列序，使下面的 INSERT 与 values 一一对应。
var agentColumns = []string{
	"id", "name", "type", "version",
	"executable_hash", "executable_path", "pid", "parent_pid", "os_user",
	"device_id", "workspace_id", "started_at", "session_key_hash", "session_expires_at",
	"trust_level", "status", "last_seen_at", "created_at", "updated_at",
}

// bindingColumns 是 REQ-AGENT-001 的九项身份绑定，注册后均非空（AC3）。
var bindingColumns = []string{
	"executable_hash", "executable_path", "pid", "parent_pid", "os_user",
	"device_id", "workspace_id", "started_at", "session_key_hash",
}

func migratedDB(t *testing.T) *DB {
	t.Helper()

	db := openForMigration(t)
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db
}

func insertDevice(ctx context.Context, db *DB, id, fingerprint, trustStatus string) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO devices (id, fingerprint, name, trust_status, created_at, updated_at)
		 VALUES (?, ?, 'MacBook', ?, ?, ?)`,
		id, fingerprint, trustStatus, testInstant, testInstant)
	return err
}

func insertWorkspace(ctx context.Context, db *DB, id, path string) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO workspaces (id, path, project_fingerprint, created_at, updated_at)
		 VALUES (?, ?, 'fp-project', ?, ?)`,
		id, path, testInstant, testInstant)
	return err
}

// seedDeviceAndWorkspace 准备 agents 的两个外键目标。
func seedDeviceAndWorkspace(t *testing.T, db *DB) {
	t.Helper()

	ctx := t.Context()
	if err := insertDevice(ctx, db, testDeviceID, "fp-device", "trusted"); err != nil {
		t.Fatalf("写入设备失败：%v", err)
	}
	if err := insertWorkspace(ctx, db, testWorkspaceID, "/Users/tester/project"); err != nil {
		t.Fatalf("写入工作区失败：%v", err)
	}
}

// agentValues 返回一行合法的 agents 数据，键与 agentColumns 一一对应。
func agentValues(id, sessionKeyHash string) map[string]any {
	return map[string]any{
		"id":                 id,
		"name":               "claude-code",
		"type":               "claude-code",
		"version":            "1.0.0",
		"executable_hash":    "sha256:abc",
		"executable_path":    "/usr/local/bin/claude",
		"pid":                4321,
		"parent_pid":         0,
		"os_user":            "tester",
		"device_id":          testDeviceID,
		"workspace_id":       testWorkspaceID,
		"started_at":         testInstant,
		"session_key_hash":   sessionKeyHash,
		"session_expires_at": testInstant,
		"trust_level":        "unverified",
		"status":             "active",
		"last_seen_at":       testInstant,
		"created_at":         testInstant,
		"updated_at":         testInstant,
	}
}

func insertAgent(ctx context.Context, db *DB, values map[string]any) error {
	arguments := make([]any, 0, len(agentColumns))
	for _, column := range agentColumns {
		arguments = append(arguments, values[column])
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(agentColumns)), ", ")
	statement := "INSERT INTO agents (" + strings.Join(agentColumns, ", ") + ") VALUES (" + placeholders + ")"

	_, err := db.Writer().ExecContext(ctx, statement, arguments...) //nolint:gosec // 列名来自本文件的常量，不来自输入
	return err
}

func TestAgents_BindingColumns_RejectNull(t *testing.T) {
	// REQ-AGENT-001 AC3：九项身份绑定注册后均非空。逐列写 NULL，每一列都必须被拒。
	ctx := t.Context()

	for _, column := range bindingColumns {
		t.Run(column, func(t *testing.T) {
			db := migratedDB(t)
			seedDeviceAndWorkspace(t, db)

			values := agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")
			values[column] = nil

			if err := insertAgent(ctx, db, values); err == nil {
				t.Errorf("%s 写入 NULL 后仍然插入成功，NOT NULL 没有生效", column)
			}
		})
	}
}

func TestAgents_BindingColumns_AllPresent_Inserts(t *testing.T) {
	// 上面的用例在建表语句整体失效时也会「通过」。这里确认合法数据确实能写进去，
	// 否则那九条断言就是永远为真的空断言。
	db := migratedDB(t)
	seedDeviceAndWorkspace(t, db)

	if err := insertAgent(t.Context(), db, agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")); err != nil {
		t.Fatalf("合法的 Agent 行被拒绝：%v", err)
	}
}

func TestAgents_ParentPidZero_IsAccepted(t *testing.T) {
	// REQ-AGENT-001 AC3 显式允许无父进程时 parent_pid 为 0，CHECK 的下界不能写成 1。
	db := migratedDB(t)
	seedDeviceAndWorkspace(t, db)

	values := agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")
	values["parent_pid"] = 0
	if err := insertAgent(t.Context(), db, values); err != nil {
		t.Errorf("parent_pid = 0 被拒绝：%v", err)
	}
}

func TestAgents_NonPositivePid_IsRejected(t *testing.T) {
	db := migratedDB(t)
	seedDeviceAndWorkspace(t, db)

	values := agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")
	values["pid"] = 0
	if err := insertAgent(t.Context(), db, values); err == nil {
		t.Error("pid = 0 被接受了，进程号必须为正")
	}
}

func TestAgents_DuplicateSessionKeyHash_IsRejected(t *testing.T) {
	// 同一个 Session Key 不可能同时属于两个 Agent，否则身份校验会认错人。
	ctx := t.Context()
	db := migratedDB(t)
	seedDeviceAndWorkspace(t, db)

	if err := insertAgent(ctx, db, agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-shared")); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertAgent(ctx, db, agentValues("01K1AGENTBBBBBBBBBBBBBBBBB", "hash-shared")); err == nil {
		t.Error("同一个会话密钥哈希被插入了第二行，唯一索引没有生效")
	}

	assertIndexIsUnique(t, db, "agents", "uq_agents_session_key_hash")
}

func TestAgents_UnknownForeignKeys_AreRejected(t *testing.T) {
	// 外键必须真的生效：foreign_keys 是连接级 PRAGMA，漏在某个连接上就会静默放行。
	ctx := t.Context()

	cases := []struct {
		name   string
		column string
	}{
		{name: "设备不存在", column: "device_id"},
		{name: "工作区不存在", column: "workspace_id"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedDeviceAndWorkspace(t, db)

			values := agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")
			values[testCase.column] = "01K1MISSINGAAAAAAAAAAAAAAA"

			if err := insertAgent(ctx, db, values); err == nil {
				t.Errorf("%s 指向不存在的行仍然插入成功", testCase.column)
			}
		})
	}
}

func TestAgents_DeletingReferencedDeviceOrWorkspace_IsRestricted(t *testing.T) {
	// 被引用的身份记录一律 RESTRICT，
	// 级联删除会让审计追溯断链。
	ctx := t.Context()

	cases := []struct {
		name      string
		statement string
		id        string
	}{
		{name: "设备", statement: "DELETE FROM devices WHERE id = ?", id: testDeviceID},
		{name: "工作区", statement: "DELETE FROM workspaces WHERE id = ?", id: testWorkspaceID},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedDeviceAndWorkspace(t, db)
			if err := insertAgent(ctx, db, agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")); err != nil {
				t.Fatalf("写入 Agent 失败：%v", err)
			}

			if _, err := db.Writer().ExecContext(ctx, testCase.statement, testCase.id); err == nil {
				t.Errorf("仍被 Agent 引用的%s被删除了", testCase.name)
			}
		})
	}
}

func TestIdentityTables_EnumColumns_RejectValuesOutsideTheirSet(t *testing.T) {
	// 枚举列的取值必须在数据库层封闭：CHECK 是这些取值的最后一道防线。
	ctx := t.Context()

	cases := []struct {
		name   string
		column string
		value  string
	}{
		{name: "未知 Agent 类型", column: "type", value: "rogue-agent"},
		{name: "未知信任等级", column: "trust_level", value: "superuser"},
		{name: "未知状态", column: "status", value: "zombie"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedDeviceAndWorkspace(t, db)

			values := agentValues("01K1AGENTAAAAAAAAAAAAAAAAA", "hash-a")
			values[testCase.column] = testCase.value

			if err := insertAgent(ctx, db, values); err == nil {
				t.Errorf("%s = %q 被接受了", testCase.column, testCase.value)
			}
		})
	}
}

func TestAgents_EveryDeclaredTypeAndTrustLevel_IsAccepted(t *testing.T) {
	// REQ-AGENT-003 AC1 的五种类型与 REQ-AGENT-002 的三个等级都必须写得进去，
	// 否则 CHECK 就把合法取值也拦掉了。
	ctx := t.Context()

	agentTypes := []string{"claude-code", "codex", "gemini-cli", "opencode", "generic"}
	trustLevels := []string{"unverified", "known", "trusted"}

	db := migratedDB(t)
	seedDeviceAndWorkspace(t, db)

	for index, agentType := range agentTypes {
		values := agentValues("01K1AGENTTYPE"+string(rune('A'+index))+"AAAAAAAAAAAA", "hash-type-"+agentType)
		values["type"] = agentType
		if err := insertAgent(ctx, db, values); err != nil {
			t.Errorf("类型 %q 被拒绝：%v", agentType, err)
		}
	}

	for index, level := range trustLevels {
		values := agentValues("01K1AGENTTRUST"+string(rune('A'+index))+"AAAAAAAAAAA", "hash-trust-"+level)
		values["trust_level"] = level
		if err := insertAgent(ctx, db, values); err != nil {
			t.Errorf("信任等级 %q 被拒绝：%v", level, err)
		}
	}
}

func TestDevices_TrustStatus_IsLimitedToItsSet(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	for _, status := range []string{"trusted", "untrusted"} {
		if err := insertDevice(ctx, db, "01K1DEV"+status, "fp-"+status, status); err != nil {
			t.Errorf("设备信任状态 %q 被拒绝：%v", status, err)
		}
	}
	if err := insertDevice(ctx, db, "01K1DEVBAD", "fp-bad", "maybe"); err == nil {
		t.Error("设备信任状态 \"maybe\" 被接受了")
	}
}

func TestDevices_DuplicateFingerprint_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertDevice(ctx, db, testDeviceID, "fp-device", "trusted"); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertDevice(ctx, db, "01K1DEVICEBBBBBBBBBBBBBBBB", "fp-device", "trusted"); err == nil {
		t.Error("同一指纹被插入了第二台设备")
	}

	assertIndexIsUnique(t, db, "devices", "uq_devices_fingerprint")
}

func TestWorkspaces_DuplicatePath_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertWorkspace(ctx, db, testWorkspaceID, "/Users/tester/project"); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertWorkspace(ctx, db, "01K1WORKSPACEBBBBBBBBBBBBB", "/Users/tester/project"); err == nil {
		t.Error("同一路径被插入了第二个工作区")
	}

	assertIndexIsUnique(t, db, "workspaces", "uq_workspaces_path")
}

func TestAgents_Version_IsTheOnlyNullableColumn(t *testing.T) {
	// version 允许为空，含义是「Agent 未上报版本」；其余列都必须是 NOT NULL。
	ctx := t.Context()
	db := migratedDB(t)

	rows, err := db.Reader().QueryContext(ctx, `SELECT name, "notnull" FROM pragma_table_info('agents')`)
	if err != nil {
		t.Fatalf("读取表结构失败：%v", err)
	}
	defer closeRows(t, rows)

	var columns int
	for rows.Next() {
		var (
			name    string
			notNull int
		)
		if err := rows.Scan(&name, &notNull); err != nil {
			t.Fatalf("解析表结构失败：%v", err)
		}
		columns++

		if name == "version" {
			if notNull == 1 {
				t.Error("version 被声明为 NOT NULL，未上报版本就无法表达")
			}
			continue
		}
		if notNull != 1 {
			t.Errorf("列 %s 可空", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败：%v", err)
	}
	if columns != len(agentColumns) {
		t.Errorf("agents 有 %d 列，期望 %d 列", columns, len(agentColumns))
	}
}

// 注册时的「这个进程之前注册过吗」查询必须走索引：它在每一次握手上跑一遍，
// 全表扫描会随 Agent 记录数线性变慢。
func TestAgents_BindingLookup_UsesTheBindingIndex(t *testing.T) {
	db := migratedDB(t)

	plan := queryPlan(t, db, `
		SELECT id FROM agents
		WHERE device_id = ? AND workspace_id = ? AND executable_path = ?
		    AND os_user = ? AND executable_hash = ?
		ORDER BY id DESC
		LIMIT 1`,
		testDeviceID, testWorkspaceID, "/usr/local/bin/claude", "tester", "sha256:6f1d0c9a2b")

	if !strings.Contains(plan, "idx_agents_binding") {
		t.Errorf("身份绑定查询没有走 idx_agents_binding：%s", plan)
	}
	// 末列 id 让排序也由索引提供；出现临时 B 树说明索引少了那一列。
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("身份绑定查询仍在临时 B 树里排序：%s", plan)
	}
}
