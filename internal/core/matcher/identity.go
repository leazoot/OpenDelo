package matcher

import (
	"context"
	"time"
)

// Environment 是身份所处的环境（REQ-INTENT-001）。
//
// 只有两个取值：判定不出来时按 production 处理（REQ-INTENT-002 AC2，就高不就低），
// 所以不需要也不允许「未知」。
type Environment string

const (
	// EnvironmentProduction 是生产环境，风险计算的输入因子之一。
	EnvironmentProduction Environment = "production"
	// EnvironmentNonProduction 是非生产环境。
	EnvironmentNonProduction Environment = "non-production"
)

// IdentityStatus 是身份当前是否可以参与自动授权。
type IdentityStatus string

const (
	// StatusOK 表示身份正常。
	StatusOK IdentityStatus = "ok"
	// StatusNeedsReview 表示检测到外部 Scope 扩大，自动授权暂停，
	// 下一次请求进入审批（REQ-IDENT-004）。
	StatusNeedsReview IdentityStatus = "needs_review"
)

// Identity 是外部服务中的一个身份，例如 GitHub Work（PRD §9.4、REQ-IDENT-001）。
type Identity struct {
	ID           string
	Service      string
	AccountLabel string
	Environment  Environment
	// IsDefault 标记同一 service 下的默认身份。它不能替代匹配：存在多个候选时
	// 必须询问用户，不得直接取默认（REQ-IDENT-002）。
	IsDefault bool
	Status    IdentityStatus
	// CredentialReferenceID 指向支撑该身份的凭据引用，不可为空 ——
	// 取不到凭据的身份匹配上了也执行不了。
	CredentialReferenceID string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// IdentityRepository 存取身份记录。
//
// 没有 Delete：身份被审计引用着（外键为 RESTRICT）。REQ-IDENT-001 AC2 说的
// 「删除 Identity 级联撤销 Lease 与 Trust Memory」是业务层的撤销，不是删行。
type IdentityRepository interface {
	// CreateIdentity 写入一个身份。同一 service 下账户名重复时返回错误。
	CreateIdentity(ctx context.Context, identity Identity) (Identity, error)
	// IdentityByID 按主键读取。不存在时返回 apperr.CodeNotFound。
	IdentityByID(ctx context.Context, id string) (Identity, error)
	// IdentityByServiceAndAccountLabel 按服务与账户名读取。
	// 不存在时返回 apperr.CodeNotFound。
	IdentityByServiceAndAccountLabel(ctx context.Context, service, accountLabel string) (Identity, error)
	// Identities 列出全部身份，服务 Identities 页面的关系视图（REQ-UI-005）。
	// limit 必须为正。
	Identities(ctx context.Context, limit int) ([]Identity, error)
	// IdentitiesForService 列出某个服务下的候选身份，最多 limit 条。
	// 结果多于一条时匹配必须上抛歧义，不得任选其一（REQ-IDENT-002）。
	IdentitiesForService(ctx context.Context, service string, limit int) ([]Identity, error)
	// SetIdentityStatus 改变身份状态（REQ-IDENT-004）。
	SetIdentityStatus(ctx context.Context, id string, status IdentityStatus, at time.Time) (Identity, error)
	// SetIdentityDefault 改变同一 service 下的默认标记。
	SetIdentityDefault(ctx context.Context, id string, isDefault bool, at time.Time) (Identity, error)
}
