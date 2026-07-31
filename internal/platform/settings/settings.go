// Package settings 存放运行期可改的偏好（REQ-PREF-001）。
//
// 与 platform/config 的分工：config 是**启动时**读进来的东西（监听地址、端口、
// 日志级别），改了要重启；settings 是**运行期**可以改且立刻生效的东西
// （自动化等级、审批超时、界面偏好）。两者都在本机，但生效方式不同，
// 混在一起会让「改完要不要重启」这个问题没有确定答案。
package settings

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// 键名。存成键值而不是一行宽表：加一个偏好不需要改 schema。
const (
	KeyAutomationMode  = "automation_mode"
	KeyApprovalTimeout = "approval_timeout_seconds"
	KeyReadOnlyAuto    = "read_only_auto_allow"
	KeyTheme           = "theme"
	KeyLanguage        = "language"
)

// 界面偏好的取值。放在后端是为了让它跨浏览器一致，语义仍归界面。
const (
	ThemeDark   = "dark"
	ThemeLight  = "light"
	ThemeSystem = "system"

	LanguageChinese = "zh"
	LanguageEnglish = "en"
)

// Preferences 是全部运行期偏好。
//
// 每一项都有默认值：配置缺失时不能让链路拿到零值 ——
// 一个零值的审批超时等于「不等就拒绝」，一个零值的自动化等级会被决策引擎
// 当成认不出的模式。
type Preferences struct {
	// AutomationMode 是决策的自动化等级（PRD §11）。
	AutomationMode decision.Mode
	// ApprovalTimeout 是等待人工确认的时长（REQ-CAP-003）。
	ApprovalTimeout time.Duration
	// ReadOnlyAutoAllow 是谨慎模式下的只读自动允许开关，默认关闭。
	ReadOnlyAutoAllow bool
	Theme             string
	Language          string
}

// Default 是全部默认值。
func Default() Preferences {
	return Preferences{
		AutomationMode:  decision.ModeBalanced,
		ApprovalTimeout: approval.DefaultTimeout,
		Theme:           ThemeSystem,
		Language:        LanguageChinese,
	}
}

// Setting 是一条键值偏好。
type Setting struct {
	ID        string
	Name      string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository 读写偏好。
//
// 没有删除方法：删掉一条偏好与把它改回默认值是同一件事，
// 而后者的意图明确得多。
type Repository interface {
	Settings(ctx context.Context) ([]Setting, error)
	// UpsertSetting 写入或覆盖一条偏好。
	UpsertSetting(ctx context.Context, setting Setting) (Setting, error)
}

// Options 是 Store 的依赖，全部必填。
type Options struct {
	Settings Repository
	Clock    clock.Clock
	IDs      *ulid.Generator
}

// Store 读写偏好并把它们解释成有类型的值。
type Store struct {
	settings Repository
	clock    clock.Clock
	ids      *ulid.Generator
}

// NewStore 校验依赖并构造 Store。
func NewStore(options Options) (*Store, error) {
	switch {
	case options.Settings == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("偏好 Store 缺少仓储")
	case options.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("偏好 Store 缺少时钟")
	case options.IDs == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("偏好 Store 缺少 ID 生成器")
	}
	return &Store{settings: options.Settings, clock: options.Clock, ids: options.IDs}, nil
}

// Load 读出当前偏好。
//
// 认不出的取值退回默认值而不是报错（REQ-PREF-001 AC3：配置损坏时用默认值
// 启动并告警，不崩溃）。退回的是**更严格**的那一侧 —— 默认的平衡模式不比
// 自动模式宽松，默认的超时也不比配置的长。
func (s *Store) Load(ctx context.Context) (Preferences, []string, error) {
	stored, err := s.settings.Settings(ctx)
	if err != nil {
		return Preferences{}, nil, err
	}

	preferences := Default()
	var warnings []string
	for _, setting := range stored {
		if warning := apply(&preferences, setting); warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return preferences, warnings, nil
}

// apply 把一条键值写进 Preferences，认不出时返回一句告警。
func apply(into *Preferences, setting Setting) string {
	switch setting.Name {
	case KeyAutomationMode:
		mode := decision.Mode(setting.Value)
		if !decision.ValidMode(mode) {
			return unreadable(setting)
		}
		into.AutomationMode = mode
	case KeyApprovalTimeout:
		timeout, err := parseTimeout(setting.Value)
		if err != nil {
			return unreadable(setting)
		}
		into.ApprovalTimeout = timeout
	case KeyReadOnlyAuto:
		enabled, err := strconv.ParseBool(setting.Value)
		if err != nil {
			return unreadable(setting)
		}
		into.ReadOnlyAutoAllow = enabled
	case KeyTheme:
		if !validTheme(setting.Value) {
			return unreadable(setting)
		}
		into.Theme = setting.Value
	case KeyLanguage:
		if !validLanguage(setting.Value) {
			return unreadable(setting)
		}
		into.Language = setting.Value
	default:
		return "偏好 " + setting.Name + " 认不出来，已忽略"
	}
	return ""
}

func unreadable(setting Setting) string {
	return "偏好 " + setting.Name + " 的取值读不出来，已改用默认值"
}

// Save 写入一批偏好。
//
// 任何一项不合法就整批不写：写一半会让「我改了什么」与「实际生效了什么」
// 对不上，而其中一项恰好是自动化等级。
func (s *Store) Save(ctx context.Context, changes map[string]string) (Preferences, error) {
	for name, value := range changes {
		if err := validate(name, value); err != nil {
			return Preferences{}, err
		}
	}

	now := s.clock.Now()
	for _, name := range ordered(changes) {
		id, err := s.ids.NewID()
		if err != nil {
			return Preferences{}, err
		}
		if _, err = s.settings.UpsertSetting(ctx, Setting{
			ID: id, Name: name, Value: changes[name], CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return Preferences{}, err
		}
	}

	saved, _, err := s.Load(ctx)
	return saved, err
}

// ordered 把键名排序后返回，让写入顺序确定，用例可以逐条断言。
func ordered(changes map[string]string) []string {
	names := make([]string, 0, len(changes))
	for name := range changes {
		names = append(names, name)
	}
	for outer := 1; outer < len(names); outer++ {
		for inner := outer; inner > 0 && names[inner] < names[inner-1]; inner-- {
			names[inner], names[inner-1] = names[inner-1], names[inner]
		}
	}
	return names
}

// validate 校验一条偏好。认不出的键名一律拒绝 ——
// 悄悄忽略会让调用方以为自己改了什么。
func validate(name, value string) error {
	switch name {
	case KeyAutomationMode:
		if !decision.ValidMode(decision.Mode(value)) {
			return invalid(name, "只能是 cautious、balanced 或 automatic")
		}
	case KeyApprovalTimeout:
		if _, err := parseTimeout(value); err != nil {
			return invalid(name, "必须是 "+
				strconv.Itoa(int(approval.MinTimeout.Seconds()))+" 到 "+
				strconv.Itoa(int(approval.MaxTimeout.Seconds()))+" 之间的秒数")
		}
	case KeyReadOnlyAuto:
		if _, err := strconv.ParseBool(value); err != nil {
			return invalid(name, "只能是 true 或 false")
		}
	case KeyTheme:
		if !validTheme(value) {
			return invalid(name, "只能是 dark、light 或 system")
		}
	case KeyLanguage:
		if !validLanguage(value) {
			return invalid(name, "只能是 zh 或 en")
		}
	default:
		return invalid(name, "不是一项已知的偏好")
	}
	return nil
}

// parseTimeout 解析审批超时并校验范围。
//
// 范围与 core/approval 的一致：一个 24 小时的审批窗口等于一条实际上
// 不会过期的授权入口，那条限制不能靠偏好绕开。
func parseTimeout(value string) (time.Duration, error) {
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0, invalid(KeyApprovalTimeout, "不是整数")
	}

	timeout := time.Duration(seconds) * time.Second
	if timeout < approval.MinTimeout || timeout > approval.MaxTimeout {
		return 0, invalid(KeyApprovalTimeout, "超出允许范围")
	}
	return timeout, nil
}

func validTheme(value string) bool {
	return value == ThemeDark || value == ThemeLight || value == ThemeSystem
}

func validLanguage(value string) bool {
	return value == LanguageChinese || value == LanguageEnglish
}

func invalid(name, detail string) error {
	return apperr.New(apperr.CodeInvalidRequest).
		WithDetail("偏好 " + strings.TrimSpace(name) + " " + detail)
}
