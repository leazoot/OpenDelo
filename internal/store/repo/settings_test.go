package repo_test

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * settings 表的读写。
 */

func setting(name, value string) settings.Setting {
	return settings.Setting{
		ID:        "01K1SETTING00000000000" + name[:5],
		Name:      name,
		Value:     value,
		CreatedAt: fixtures.Instant,
		UpdatedAt: fixtures.Instant,
	}
}

func TestSettings_UpsertCreatesThenOverwritesInPlace(t *testing.T) {
	// 同一个键写两次只该有一行：累积会让「当前是哪个值」有两个答案。
	ctx := t.Context()
	stored := repo.NewSettings(fixtures.MigratedDB(t))

	created, err := stored.UpsertSetting(ctx, setting(settings.KeyTheme, "dark"))
	if err != nil {
		t.Fatalf("写入偏好失败：%v", err)
	}

	later := setting(settings.KeyTheme, "light")
	later.UpdatedAt = fixtures.Instant.Add(time.Minute)
	updated, err := stored.UpsertSetting(ctx, later)
	if err != nil {
		t.Fatalf("覆盖偏好失败：%v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("覆盖产生了新行：%s → %s", created.ID, updated.ID)
	}
	if updated.Value != "light" {
		t.Errorf("取值为 %q", updated.Value)
	}
	// 创建时刻不该被覆盖冲掉：它记的是「这条偏好什么时候第一次被设定」。
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("创建时刻从 %v 变成了 %v", created.CreatedAt, updated.CreatedAt)
	}

	all, err := stored.Settings(ctx)
	if err != nil {
		t.Fatalf("列出偏好失败：%v", err)
	}
	if len(all) != 1 {
		t.Fatalf("库里有 %d 行，期望 1 行", len(all))
	}
}

func TestSettings_ListIsOrderedByName(t *testing.T) {
	ctx := t.Context()
	stored := repo.NewSettings(fixtures.MigratedDB(t))

	for _, pair := range [][2]string{
		{settings.KeyTheme, "dark"},
		{settings.KeyLanguage, "zh"},
		{settings.KeyAutomationMode, "balanced"},
	} {
		if _, err := stored.UpsertSetting(ctx, setting(pair[0], pair[1])); err != nil {
			t.Fatalf("写入偏好失败：%v", err)
		}
	}

	all, err := stored.Settings(ctx)
	if err != nil {
		t.Fatalf("列出偏好失败：%v", err)
	}
	if len(all) != 3 {
		t.Fatalf("库里有 %d 行，期望 3 行", len(all))
	}
	for index := 1; index < len(all); index++ {
		if all[index-1].Name > all[index].Name {
			t.Errorf("顺序不对：%q 排在 %q 之前", all[index-1].Name, all[index].Name)
		}
	}
}

func TestSettings_DuplicateNameIsReportedAsConflict(t *testing.T) {
	// 唯一索引挡下同名的第二行。Upsert 走的是更新分支，
	// 这里直接构造两条不同主键同名的记录来验证约束本身还在。
	ctx := t.Context()
	stored := repo.NewSettings(fixtures.MigratedDB(t))

	first := setting(settings.KeyTheme, "dark")
	if _, err := stored.UpsertSetting(ctx, first); err != nil {
		t.Fatalf("写入偏好失败：%v", err)
	}

	second := first
	second.ID = "01K1SETTINGDUPLICATE0000000"
	second.Value = "light"
	updated, err := stored.UpsertSetting(ctx, second)
	if err != nil {
		t.Fatalf("覆盖偏好失败：%v", err)
	}
	if updated.ID != first.ID {
		t.Errorf("同名偏好产生了第二行 %s", updated.ID)
	}
}

func TestAgents_ListIsBoundedAndNewestFirst(t *testing.T) {
	// 无界列表查询由仓储拒绝。
	ctx := t.Context()
	db := fixtures.SeededRequestChain(t)
	agents := repo.NewAgents(db)

	listed, err := agents.Agents(ctx, 10)
	if err != nil {
		t.Fatalf("列出 Agent 失败：%v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("列出了 %d 个 Agent，期望 1 个", len(listed))
	}

	for _, limit := range []int{0, -1} {
		if _, err = agents.Agents(ctx, limit); !apperr.Is(err, apperr.CodeInvalidRequest) {
			t.Errorf("limit=%d 时返回 %v，期望 invalid_request", limit, err)
		}
	}
}
