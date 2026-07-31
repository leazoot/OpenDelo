package settings_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 运行期偏好（REQ-PREF-001）。
 *
 * 用真实的 SQLite 仓储：「写一半」与「认不出的取值退回默认值」都要落到
 * 真正的读写路径上才测得到。
 */

type harness struct {
	store    *settings.Store
	settings *repo.Settings
	clock    *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	stored := repo.NewSettings(db)

	store, err := settings.NewStore(settings.Options{
		Settings: stored, Clock: fixed, IDs: ulid.New(fixed),
	})
	if err != nil {
		t.Fatalf("构造偏好 Store 失败：%v", err)
	}
	return harness{store: store, settings: stored, clock: fixed}
}

func load(t *testing.T, all harness) settings.Preferences {
	t.Helper()

	current, warnings, err := all.store.Load(t.Context())
	if err != nil {
		t.Fatalf("读取偏好失败：%v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("读出了预期之外的告警：%v", warnings)
	}
	return current
}

func TestLoad_WithNothingStored_ReturnsTheConservativeDefaults(t *testing.T) {
	// 默认值不能是零值：零值的审批超时等于「不等就拒绝」，
	// 零值的自动化等级会被决策引擎当成认不出的模式并拒绝一切。
	all := newHarness(t)

	current := load(t, all)
	if current.AutomationMode != decision.ModeBalanced {
		t.Errorf("自动化等级为 %s", current.AutomationMode)
	}
	if current.ApprovalTimeout != approval.DefaultTimeout {
		t.Errorf("审批超时为 %v", current.ApprovalTimeout)
	}
	if current.ReadOnlyAutoAllow {
		t.Error("只读自动允许默认是开的")
	}
	if current.Theme != settings.ThemeSystem || current.Language != settings.LanguageChinese {
		t.Errorf("界面偏好默认值为 %q / %q", current.Theme, current.Language)
	}
}

func TestSaveThenLoad_RoundTripsEveryKey(t *testing.T) {
	all := newHarness(t)

	saved, err := all.store.Save(t.Context(), map[string]string{
		settings.KeyAutomationMode:  string(decision.ModeCautious),
		settings.KeyApprovalTimeout: "90",
		settings.KeyReadOnlyAuto:    "true",
		settings.KeyTheme:           settings.ThemeLight,
		settings.KeyLanguage:        settings.LanguageEnglish,
	})
	if err != nil {
		t.Fatalf("写入偏好失败：%v", err)
	}
	if saved.AutomationMode != decision.ModeCautious ||
		saved.ApprovalTimeout != 90*time.Second ||
		!saved.ReadOnlyAutoAllow ||
		saved.Theme != settings.ThemeLight ||
		saved.Language != settings.LanguageEnglish {
		t.Fatalf("写入结果为 %+v", saved)
	}

	if reread := load(t, all); reread != saved {
		t.Errorf("重新读出来的是 %+v，刚写进去的是 %+v", reread, saved)
	}
}

func TestSave_OverwritesInsteadOfAccumulating(t *testing.T) {
	// 同一个键写两次只该有一行：累积会让「当前是哪个值」有两个答案。
	all := newHarness(t)

	for _, theme := range []string{settings.ThemeLight, settings.ThemeDark} {
		if _, err := all.store.Save(
			t.Context(), map[string]string{settings.KeyTheme: theme}); err != nil {
			t.Fatalf("写入偏好失败：%v", err)
		}
	}

	stored, err := all.settings.Settings(t.Context())
	if err != nil {
		t.Fatalf("列出偏好失败：%v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("库里有 %d 行偏好，期望 1 行：%v", len(stored), stored)
	}
	if load(t, all).Theme != settings.ThemeDark {
		t.Error("第二次写入没有覆盖第一次")
	}
}

func TestSave_AnythingUnrecognised_WritesNothingAtAll(t *testing.T) {
	// 写一半会让「我改了什么」与「实际生效了什么」对不上，
	// 而其中一项恰好是自动化等级。
	cases := map[string]map[string]string{
		"认不出的键名": {"turn_off_audit": "true"},
		"认不出的等级": {settings.KeyAutomationMode: "yolo"},
		"超时不是整数": {settings.KeyApprovalTimeout: "一会儿"},
		"超时太长":   {settings.KeyApprovalTimeout: "86400"},
		"超时太短":   {settings.KeyApprovalTimeout: "1"},
		"开关认不出":  {settings.KeyReadOnlyAuto: "maybe"},
		"主题认不出":  {settings.KeyTheme: "neon"},
		"语言认不出":  {settings.KeyLanguage: "kl"},
		"一好一坏": {
			settings.KeyTheme:          settings.ThemeLight,
			settings.KeyAutomationMode: "yolo",
		},
	}

	for name, changes := range cases {
		t.Run(name, func(t *testing.T) {
			fresh := newHarness(t)
			if _, err := fresh.store.Save(t.Context(), changes); !apperr.Is(
				err, apperr.CodeInvalidRequest) {
				t.Fatalf("错误码为 %v，期望 invalid_request", err)
			}

			stored, err := fresh.settings.Settings(t.Context())
			if err != nil {
				t.Fatalf("列出偏好失败：%v", err)
			}
			if len(stored) != 0 {
				t.Errorf("写进去了 %d 行：%v", len(stored), stored)
			}
		})
	}
}

func TestSave_ApprovalTimeoutStaysInsideTheRangeCoreAllows(t *testing.T) {
	// 一个 24 小时的审批窗口等于一条实际上不会过期的授权入口，
	// core/approval 的那条限制不能靠偏好绕开。边界值逐个断言。
	cases := map[string]bool{
		"29":   false,
		"30":   true,
		"1800": true,
		"1801": false,
	}

	for value, allowed := range cases {
		t.Run(value, func(t *testing.T) {
			fresh := newHarness(t)
			_, err := fresh.store.Save(
				t.Context(), map[string]string{settings.KeyApprovalTimeout: value})
			if allowed && err != nil {
				t.Fatalf("%s 秒被拒绝了：%v", value, err)
			}
			if !allowed && err == nil {
				t.Fatalf("%s 秒被接受了，而它在 core/approval 的范围之外", value)
			}
		})
	}

	if approval.MinTimeout != 30*time.Second || approval.MaxTimeout != 30*time.Minute {
		t.Errorf("core/approval 的范围变了（%v ~ %v），这条用例的边界要跟着改",
			approval.MinTimeout, approval.MaxTimeout)
	}
}

func TestLoad_UnreadableStoredValue_FallsBackToDefaultAndWarns(t *testing.T) {
	// REQ-PREF-001 AC3：配置损坏时用默认值继续并告警，不崩溃。
	// 退回的是**更严格**的那一侧 —— 平衡模式不比自动模式宽松。
	all := newHarness(t)

	for _, broken := range []settings.Setting{
		{ID: "01K1SETTING0000000000000001", Name: settings.KeyAutomationMode, Value: "yolo"},
		{ID: "01K1SETTING0000000000000002", Name: settings.KeyApprovalTimeout, Value: "86400"},
		{ID: "01K1SETTING0000000000000003", Name: "已经不存在的偏好", Value: "1"},
	} {
		broken.CreatedAt = fixtures.Instant
		broken.UpdatedAt = fixtures.Instant
		if _, err := all.settings.UpsertSetting(t.Context(), broken); err != nil {
			t.Fatalf("写入损坏的偏好失败：%v", err)
		}
	}

	current, warnings, err := all.store.Load(t.Context())
	if err != nil {
		t.Fatalf("读取偏好失败：%v", err)
	}
	if len(warnings) != 3 {
		t.Errorf("读出了 %d 条告警，期望 3 条：%v", len(warnings), warnings)
	}
	if current.AutomationMode != decision.ModeBalanced {
		t.Errorf("认不出的等级没有退回默认值，而是 %s", current.AutomationMode)
	}
	if current.ApprovalTimeout != approval.DefaultTimeout {
		t.Errorf("超范围的超时没有退回默认值，而是 %v", current.ApprovalTimeout)
	}
}

func TestNewStore_MissingAnyDependency_IsRefused(t *testing.T) {
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	complete := settings.Options{
		Settings: repo.NewSettings(db), Clock: fixed, IDs: ulid.New(fixed),
	}

	for name, blank := range map[string]func(*settings.Options){
		"Settings": func(o *settings.Options) { o.Settings = nil },
		"Clock":    func(o *settings.Options) { o.Clock = nil },
		"IDs":      func(o *settings.Options) { o.IDs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			options := complete
			blank(&options)
			if _, err := settings.NewStore(options); err == nil {
				t.Errorf("%s 为空时仍然构造出了 Store", name)
			}
		})
	}
}

func TestDefault_MatchesWhatTheDecisionChainConsidersValid(t *testing.T) {
	// 默认的自动化等级必须是决策引擎认得的三个之一，否则一台全新安装的
	// Gateway 会把每一次请求都当成策略引擎异常并拒绝。
	if !decision.ValidMode(settings.Default().AutomationMode) {
		t.Errorf("默认自动化等级 %s 不是决策引擎认得的取值",
			settings.Default().AutomationMode)
	}
	if strings.TrimSpace(settings.Default().Theme) == "" {
		t.Error("默认主题为空")
	}
}
