package queries_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

const (
	createdAt = "2026-07-28T09:15:30.123Z"
	updatedAt = "2026-07-28T10:00:00.000Z"
)

// migratedDatabase 在独立的临时目录里开一个已迁移的数据库。
// 每个用例一份，用例之间不共享任何状态。
func migratedDatabase(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), store.FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭数据库失败：%v", err)
		}
	})

	if _, err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db
}

func createTheme(t *testing.T, ctx context.Context, writer *queries.Queries, value string) queries.Setting {
	t.Helper()

	created, err := writer.CreateSetting(ctx, queries.CreateSettingParams{
		ID:        "01K1AAAAAAAAAAAAAAAAAAAAAA",
		Name:      "theme",
		Value:     value,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("写入偏好失败：%v", err)
	}
	return created
}

func TestSettings_CreateThenGet_RoundTripsEveryColumn(t *testing.T) {
	ctx := t.Context()
	db := migratedDatabase(t)
	writer := queries.New(db.Writer())

	created := createTheme(t, ctx, writer, `"dark"`)

	got, err := queries.New(db.Reader()).GetSetting(ctx, "theme")
	if err != nil {
		t.Fatalf("读取偏好失败：%v", err)
	}
	if got != created {
		t.Errorf("读到 %+v，期望 %+v", got, created)
	}
	// RFC3339 时间以 TEXT 原样往返，驱动不做隐式时区或格式转换。
	if got.CreatedAt != createdAt {
		t.Errorf("created_at 为 %q，期望 %q", got.CreatedAt, createdAt)
	}
}

func TestSettings_Get_AbsentName_ReturnsNoRows(t *testing.T) {
	db := migratedDatabase(t)

	_, err := queries.New(db.Reader()).GetSetting(t.Context(), "absent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("错误为 %v，期望 sql.ErrNoRows", err)
	}
}

func TestSettings_UpdateValue_KeepsIdentityAndCreatedAt(t *testing.T) {
	ctx := t.Context()
	db := migratedDatabase(t)
	writer := queries.New(db.Writer())

	created := createTheme(t, ctx, writer, `"dark"`)

	updated, err := writer.UpdateSettingValue(ctx, queries.UpdateSettingValueParams{
		Value:     `"light"`,
		UpdatedAt: updatedAt,
		Name:      "theme",
	})
	if err != nil {
		t.Fatalf("更新偏好失败：%v", err)
	}

	switch {
	case updated.Value != `"light"`:
		t.Errorf("value 为 %q，期望 %q", updated.Value, `"light"`)
	case updated.UpdatedAt != updatedAt:
		t.Errorf("updated_at 为 %q，期望 %q", updated.UpdatedAt, updatedAt)
	case updated.ID != created.ID:
		t.Errorf("id 从 %q 变成 %q", created.ID, updated.ID)
	case updated.CreatedAt != created.CreatedAt:
		t.Errorf("created_at 从 %q 变成 %q", created.CreatedAt, updated.CreatedAt)
	}
}

func TestSettings_UpdateValue_AbsentName_ReturnsNoRows(t *testing.T) {
	// 更新不存在的偏好不应悄悄变成一次插入，也不应当成成功。
	db := migratedDatabase(t)

	_, err := queries.New(db.Writer()).UpdateSettingValue(t.Context(), queries.UpdateSettingValueParams{
		Value:     `"light"`,
		UpdatedAt: updatedAt,
		Name:      "absent",
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("错误为 %v，期望 sql.ErrNoRows", err)
	}
}

func TestSettings_Delete_RemovesTheRow(t *testing.T) {
	ctx := t.Context()
	db := migratedDatabase(t)
	writer := queries.New(db.Writer())

	createTheme(t, ctx, writer, `"dark"`)

	if err := writer.DeleteSetting(ctx, "theme"); err != nil {
		t.Fatalf("删除偏好失败：%v", err)
	}
	if _, err := queries.New(db.Reader()).GetSetting(ctx, "theme"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("删除后仍读到记录，错误为 %v", err)
	}
}

func TestSettings_Delete_AbsentName_IsNotAnError(t *testing.T) {
	// DELETE 是幂等的：重复收回同一个偏好不该报错。
	if err := queries.New(migratedDatabase(t).Writer()).DeleteSetting(t.Context(), "absent"); err != nil {
		t.Errorf("删除不存在的偏好报错：%v", err)
	}
}

func TestSettings_List_IsOrderedByNameAndEmptyWhenNoRows(t *testing.T) {
	ctx := t.Context()
	db := migratedDatabase(t)
	writer := queries.New(db.Writer())
	reader := queries.New(db.Reader())

	empty, err := reader.ListSettings(ctx)
	if err != nil {
		t.Fatalf("列出偏好失败：%v", err)
	}
	if empty == nil {
		t.Error("空结果为 nil，期望空切片（emit_empty_slices）")
	}
	if len(empty) != 0 {
		t.Errorf("空库里列出了 %d 条偏好", len(empty))
	}

	for id, name := range map[string]string{
		"01K1BBBBBBBBBBBBBBBBBBBBBB": "theme",
		"01K1CCCCCCCCCCCCCCCCCCCCCC": "language",
		"01K1DDDDDDDDDDDDDDDDDDDDDD": "automation_level",
	} {
		if _, createErr := writer.CreateSetting(ctx, queries.CreateSettingParams{
			ID: id, Name: name, Value: `"x"`, CreatedAt: createdAt, UpdatedAt: createdAt,
		}); createErr != nil {
			t.Fatalf("写入偏好 %s 失败：%v", name, createErr)
		}
	}

	listed, err := reader.ListSettings(ctx)
	if err != nil {
		t.Fatalf("列出偏好失败：%v", err)
	}
	names := make([]string, 0, len(listed))
	for _, setting := range listed {
		names = append(names, setting.Name)
	}
	if want := "automation_level,language,theme"; strings.Join(names, ",") != want {
		t.Errorf("顺序为 %v，期望 %s", names, want)
	}
}

func TestSettings_Create_DuplicateName_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDatabase(t)
	writer := queries.New(db.Writer())

	createTheme(t, ctx, writer, `"dark"`)

	if _, err := writer.CreateSetting(ctx, queries.CreateSettingParams{
		ID: "01K1EEEEEEEEEEEEEEEEEEEEEE", Name: "theme", Value: `"light"`,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err == nil {
		t.Error("同名偏好被写入了第二行")
	}
}

func TestSettings_WithTx_RollbackDiscardsWrites(t *testing.T) {
	// 生成代码的 WithTx 是后续「决策 → 审批 → Lease → 审计」写在一个事务里的基础，
	// 这里先验证它确实绑在事务上。
	ctx := t.Context()
	db := migratedDatabase(t)

	tx, err := db.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开启事务失败：%v", err)
	}
	if _, err := queries.New(db.Writer()).WithTx(tx).CreateSetting(ctx, queries.CreateSettingParams{
		ID: "01K1FFFFFFFFFFFFFFFFFFFFFF", Name: "theme", Value: `"dark"`,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("事务内写入失败：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("回滚事务失败：%v", err)
	}

	if _, err := queries.New(db.Reader()).GetSetting(ctx, "theme"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("回滚后仍读到记录，错误为 %v", err)
	}
}

func TestQuerySources_AreASCIIOnly(t *testing.T) {
	// sqlc v1.27 的 SQLite 引擎按字节偏移切分查询、按 rune 剥离注释，`.sql` 里一个
	// 中文字符就会让后续查询的 SQL 常量被错位截断。那种产物照样能编译、能通过
	// lint，只在运行时报语法错误 —— 所以这条约束必须由用例守住，见 doc.go。
	sources, err := filepath.Glob("*.sql")
	if err != nil {
		t.Fatalf("列出 SQL 文件失败：%v", err)
	}
	if len(sources) == 0 {
		t.Fatal("没有找到任何 SQL 文件，检查等于没做")
	}

	for _, name := range sources {
		content, err := os.ReadFile(name) //nolint:gosec // 文件名来自本目录的 glob
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		for offset, char := range string(content) {
			if char > unicode.MaxASCII {
				t.Errorf("%s 第 %d 字节处出现非 ASCII 字符 %q", name, offset, char)
			}
		}
	}
}

func TestGeneratedQueries_ContainNoSelectStar(t *testing.T) {
	// AC4。SQL 输入与生成产物都扫：前者是源头，后者是真正发给 SQLite 的语句。
	selectStar := regexp.MustCompile(`(?i)select\s+\*`)

	for _, name := range []string{"settings.sql", "settings.sql.go", "db.go", "models.go"} {
		content, err := os.ReadFile(name) //nolint:gosec // 文件名是本用例里的常量
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		if match := selectStar.FindString(string(content)); match != "" {
			t.Errorf("%s 中出现 %q", name, match)
		}
	}
}
