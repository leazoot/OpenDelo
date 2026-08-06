package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/store/migrations"
)

/*
 * 迁移用例放在 store 包内部，因为 up → down → up 必须能调用未导出的 rollback：
 * 回滚不是生产入口（见 rollback 的注释），但它的 Down 段必须真的被执行过。
 */

const latestVersion int64 = 21

func openForMigration(t *testing.T) *DB {
	t.Helper()

	db, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭数据库失败：%v", err)
		}
	})
	return db
}

func TestMigrate_UpDownUp_ProducesIdenticalSchema(t *testing.T) {
	// AC1。快照取自 sqlite_master，覆盖表、索引与它们的建表语句本身。
	ctx := t.Context()
	db := openForMigration(t)

	version, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if version != latestVersion {
		t.Fatalf("迁移后版本为 %d，期望 %d", version, latestVersion)
	}
	afterFirstUp := schemaSnapshot(t, db)

	if err := rollback(ctx, db, 0); err != nil {
		t.Fatalf("回滚失败：%v", err)
	}
	afterDown := schemaSnapshot(t, db)
	if afterDown == afterFirstUp {
		t.Fatal("回滚后 schema 与迁移后相同，Down 段什么都没做")
	}
	if strings.Contains(afterDown, "settings") {
		t.Errorf("回滚后 settings 仍在：\n%s", afterDown)
	}

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("再次迁移失败：%v", err)
	}
	if afterSecondUp := schemaSnapshot(t, db); afterSecondUp != afterFirstUp {
		t.Errorf("up → down → up 后 schema 不一致：\n--- 首次 ---\n%s\n--- 再次 ---\n%s", afterFirstUp, afterSecondUp)
	}
}

func TestMigrate_RunTwice_DoesNotReapplyMigrations(t *testing.T) {
	// AC3：版本记录在 goose 版本表中，重复启动不重复执行。
	// 若迁移被重跑，CREATE TABLE settings 会因表已存在而直接报错。
	ctx := t.Context()
	db := openForMigration(t)

	first, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	appliedAfterFirst := appliedVersions(t, db)

	second, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	if second != first {
		t.Errorf("重复迁移后版本从 %d 变成 %d", first, second)
	}
	if appliedAfterSecond := appliedVersions(t, db); appliedAfterSecond != appliedAfterFirst {
		t.Errorf("版本表记录数从 %d 变成 %d，迁移被重复执行了", appliedAfterFirst, appliedAfterSecond)
	}
}

func TestMigrate_ReadsMigrationsFromEmbeddedFS_NotFromDisk(t *testing.T) {
	// AC2：把工作目录换到一个空目录，迁移仍然能跑通 —— 证明文件来自二进制而非磁盘。
	t.Chdir(t.TempDir())

	db := openForMigration(t)
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("在空工作目录下迁移失败：%v", err)
	}
	assertTableExists(t, db, "settings")
}

func TestMigrate_SettingsTable_MatchesDatabaseRules(t *testing.T) {
	// AC4：命名、ULID 主键、时间字段、非空、唯一索引。
	ctx := t.Context()
	db := openForMigration(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	wantColumns := []struct {
		name       string
		columnType string
		notNull    bool
		primaryKey bool
	}{
		{name: "id", columnType: "TEXT", notNull: true, primaryKey: true},
		{name: "name", columnType: "TEXT", notNull: true},
		{name: "value", columnType: "TEXT", notNull: true},
		{name: "created_at", columnType: "TEXT", notNull: true},
		{name: "updated_at", columnType: "TEXT", notNull: true},
	}

	rows, err := db.Reader().QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info('settings')`)
	if err != nil {
		t.Fatalf("读取表结构失败：%v", err)
	}
	defer closeRows(t, rows)

	var index int
	for rows.Next() {
		var (
			name, columnType   string
			notNull, primarKey int
		)
		if err := rows.Scan(&name, &columnType, &notNull, &primarKey); err != nil {
			t.Fatalf("解析表结构失败：%v", err)
		}
		if index >= len(wantColumns) {
			t.Errorf("多出一列 %q", name)
			continue
		}

		want := wantColumns[index]
		switch {
		case name != want.name:
			t.Errorf("第 %d 列是 %q，期望 %q", index, name, want.name)
		case columnType != want.columnType:
			t.Errorf("%s 的类型是 %q，期望 %q", name, columnType, want.columnType)
		case (notNull == 1) != want.notNull:
			t.Errorf("%s 的 NOT NULL 为 %v，期望 %v", name, notNull == 1, want.notNull)
		case (primarKey == 1) != want.primaryKey:
			t.Errorf("%s 的主键标记为 %v，期望 %v", name, primarKey == 1, want.primaryKey)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败：%v", err)
	}
	if index != len(wantColumns) {
		t.Errorf("settings 有 %d 列，期望 %d 列", index, len(wantColumns))
	}

	assertIndexIsUnique(t, db, "settings", "uq_settings_name")
}

func TestMigrate_SettingsTable_RejectsDuplicateName(t *testing.T) {
	// 唯一索引不只是存在，还得真的拦住第二行。
	ctx := t.Context()
	db := openForMigration(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	const insert = `INSERT INTO settings (id, name, value, created_at, updated_at)
		VALUES (?, 'theme', '"dark"', '2026-07-28T00:00:00.000Z', '2026-07-28T00:00:00.000Z')`

	if _, err := db.Writer().ExecContext(ctx, insert, "01JZZZZZZZZZZZZZZZZZZZZZZ1"); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, insert, "01JZZZZZZZZZZZZZZZZZZZZZZ2"); err == nil {
		t.Error("同名偏好被插入了第二行，uq_settings_name 没有生效")
	}
}

func TestMigrationFiles_EachHasUpAndDownSection(t *testing.T) {
	// Down 必须真正可回滚，不允许 -- no rollback。
	// 这条断言对将来每一个新迁移都生效。
	entries := migrationFileNames(t)
	if len(entries) == 0 {
		t.Fatal("没有嵌入任何迁移文件")
	}

	for _, name := range entries {
		content, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		source := string(content)

		for _, marker := range []string{"-- +goose Up", "-- +goose Down"} {
			if !strings.Contains(source, marker) {
				t.Errorf("%s 缺少 %q", name, marker)
			}
		}
		if strings.Contains(strings.ToLower(source), "no rollback") {
			t.Errorf("%s 声明了不可回滚", name)
		}
	}
}

func TestMigrationFiles_FollowNamingConvention(t *testing.T) {
	// 迁移文件名格式：NNNNN_<动词>_<对象>.sql。
	naming := regexp.MustCompile(`^\d{5}_[a-z][a-z0-9]*(_[a-z0-9]+)+\.sql$`)

	for _, name := range migrationFileNames(t) {
		if !naming.MatchString(name) {
			t.Errorf("迁移文件名 %q 不符合 NNNNN_<动词>_<对象>.sql", name)
		}
	}
}

func migrationFileNames(t *testing.T) []string {
	t.Helper()

	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("列出迁移文件失败：%v", err)
	}
	return entries
}

// schemaSnapshot 返回 sqlite_master 中的用户对象，排除 goose 的版本表与
// SQLite 为唯一约束自动创建的索引（后者的名字含自增序号，与迁移无关）。
func schemaSnapshot(t *testing.T, db *DB) string {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(), `
		SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE name NOT LIKE 'goose_db_version%' AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("读取 schema 失败：%v", err)
	}
	defer closeRows(t, rows)

	var snapshot strings.Builder
	for rows.Next() {
		var objectType, name, table, statement string
		if err := rows.Scan(&objectType, &name, &table, &statement); err != nil {
			t.Fatalf("解析 schema 失败：%v", err)
		}
		snapshot.WriteString(objectType + "\t" + name + "\t" + table + "\t" + statement + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 schema 失败：%v", err)
	}
	return snapshot.String()
}

func appliedVersions(t *testing.T, db *DB) int {
	t.Helper()

	var applied int
	if err := db.Reader().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM goose_db_version`).Scan(&applied); err != nil {
		t.Fatalf("读取版本表失败：%v", err)
	}
	return applied
}

func assertTableExists(t *testing.T, db *DB, table string) {
	t.Helper()

	var found int
	if err := db.Reader().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil {
		t.Fatalf("查询表 %s 失败：%v", table, err)
	}
	if found != 1 {
		t.Errorf("表 %s 不存在", table)
	}
}

func assertIndexIsUnique(t *testing.T, db *DB, table, index string) {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT name, "unique" FROM pragma_index_list(?)`, table)
	if err != nil {
		t.Fatalf("读取索引列表失败：%v", err)
	}
	defer closeRows(t, rows)

	for rows.Next() {
		var (
			name     string
			isUnique int
		)
		if err := rows.Scan(&name, &isUnique); err != nil {
			t.Fatalf("解析索引列表失败：%v", err)
		}
		if name != index {
			continue
		}
		if isUnique != 1 {
			t.Errorf("索引 %s 不是唯一索引", index)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("遍历索引列表失败：%v", err)
		}
		return
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历索引列表失败：%v", err)
	}
	t.Errorf("索引 %s 不存在", index)
}

func closeRows(t *testing.T, rows *sql.Rows) {
	t.Helper()

	if err := rows.Close(); err != nil {
		t.Errorf("关闭结果集失败：%v", err)
	}
}
