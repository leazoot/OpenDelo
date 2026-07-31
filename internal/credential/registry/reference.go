package registry

import (
	"context"
	"time"
)

/*
 * 凭据来源与引用的领域模型，以及它们的仓储接口。
 *
 * 接口定义在使用方一侧、实现在 internal/store/repo（依赖倒置，
 * 依赖倒置）。放在 credential 而不是 core：取用凭据是本包的
 * 职责，而依赖方向只允许 core → credential。
 *
 * 这里的任何类型都不承载凭据明文。明文只以 platform/secret.Value 存在，
 * 且只在取用期间存在（REQ-CRED-001）。
 */

// ProviderKind 是凭据来源的种类。
type ProviderKind string

// 本期实现的三种来源（REQ-CRED-006 AC1）。PRD §9.2 列出的其余五种本期不实现，
// 因此也没有对应取值。
const (
	KindOnePassword   ProviderKind = "1password"
	KindMacOSKeychain ProviderKind = "macos-keychain"
	KindLocalVault    ProviderKind = "local-vault"
)

// HealthStatus 是一份凭据引用当前是否可用（REQ-CRED-005）。
type HealthStatus string

const (
	// HealthOK 表示上次验证通过。
	HealthOK HealthStatus = "ok"
	// HealthNeedsReauth 表示需要重新认证。依赖它的 Trust Memory 暂停生效
	// （REQ-CRED-005 AC2）。
	HealthNeedsReauth HealthStatus = "needs_reauth"
	// HealthUnavailable 表示来源当前取不到凭据。请求一律拒绝，不得放行
	HealthUnavailable HealthStatus = "unavailable"
)

// Provider 是一个凭据来源。它描述「去哪里取」，本身不含任何凭据。
type Provider struct {
	ID   string
	Kind ProviderKind
	// Label 是用户可读的名字，用于区分同一种类下的多个来源。
	Label     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Reference 是一份凭据的坐标（PRD §9.3）。
//
// ProviderID + ItemRef + Field 三者构成引用；凭它无法离线还原出任何 Secret，
// 明文永远来自 Provider 的实时取用（REQ-CRED-001）。
type Reference struct {
	ID         string
	ProviderID string
	// ItemRef 是 Provider 内部的条目坐标，形式由各 Provider 自行定义。
	ItemRef string
	// Field 是条目里的哪个字段。
	Field        string
	Service      string
	AccountLabel string
	// Metadata 是展示与匹配用的附加信息，JSON 对象文本。
	Metadata string
	// Capabilities 是这份凭据被声明可以做什么，JSON 数组文本。
	Capabilities string
	HealthStatus HealthStatus
	// LastVerifiedAt 为零值表示从未验证过，落库为 NULL。
	LastVerifiedAt time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProviderRepository 存取凭据来源。
type ProviderRepository interface {
	// CreateProvider 登记一个来源。同种类下名字重复时返回错误。
	CreateProvider(ctx context.Context, provider Provider) (Provider, error)
	// ProviderByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	ProviderByID(ctx context.Context, id string) (Provider, error)
	// ProviderByKindAndLabel 按种类与名字读取，用于登记时找回已有记录。
	ProviderByKindAndLabel(ctx context.Context, kind ProviderKind, label string) (Provider, error)
}

// ReferenceRepository 存取凭据引用。
//
// 没有 Delete：引用被 Identity 与审计引用着，删除会让追溯断链（外键为 RESTRICT）。
type ReferenceRepository interface {
	// CreateReference 登记一份引用。同一来源下的同一字段重复登记时返回错误。
	CreateReference(ctx context.Context, reference Reference) (Reference, error)
	// ReferenceByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	ReferenceByID(ctx context.Context, id string) (Reference, error)
	// SetReferenceHealth 更新健康状态与最近验证时刻。verifiedAt 为零值表示仍未验证过。
	SetReferenceHealth(
		ctx context.Context, id string, status HealthStatus, verifiedAt, at time.Time,
	) (Reference, error)
}
