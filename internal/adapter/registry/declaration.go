package registry

import (
	"context"
	"time"
)

// Kind 是 Adapter 的种类。本期只实现四种（REQ-ADAPTER-006 AC1）。
type Kind string

const (
	// KindGitHub 对应 REQ-ADAPTER-002。
	KindGitHub Kind = "github"
	// KindCloudflare 对应 REQ-ADAPTER-003。
	KindCloudflare Kind = "cloudflare"
	// KindModel 对应 OpenAI / Anthropic（REQ-ADAPTER-004）。
	KindModel Kind = "model"
	// KindGenericHTTP 对应用户自定义的 HTTP Adapter（REQ-ADAPTER-005）。
	KindGenericHTTP Kind = "generic-http"
)

// AuthScheme 是凭据注入的形式，不是凭据本身。
//
// 明文只以 platform/secret.Value 流转，且只出现在 credential 与 adapter
// 两个包的签名中；这里记录的是「注入到哪里」。
type AuthScheme string

const (
	// AuthNone 用于不需要凭据的端点。
	AuthNone AuthScheme = "none"
	// AuthBearer 注入 Authorization: Bearer。
	AuthBearer AuthScheme = "bearer"
	// AuthHeader 注入 Adapter 声明的自定义请求头。
	AuthHeader AuthScheme = "header"
)

// RiskLabel 是 Adapter 对一个操作声明的风险标签（REQ-ADAPTER-001）。
//
// Adapter 声明标签，但不计算等级 —— 等级由 core/risk 结合环境、范围、
// 信任状态等因子算出，可能高于标签。
type RiskLabel string

const (
	// RiskLabelLow 声明为读取类操作。
	RiskLabelLow RiskLabel = "low"
	// RiskLabelMedium 声明为有限范围的写操作。
	RiskLabelMedium RiskLabel = "medium"
	// RiskLabelHigh 声明为删除、不可逆或涉及权限的操作。
	RiskLabelHigh RiskLabel = "high"
)

// Status 是 Adapter 当前是否参与决策。
type Status string

const (
	// StatusEnabled 表示可以被请求命中。
	StatusEnabled Status = "enabled"
	// StatusDisabled 表示暂停使用。停用而不是删除，否则审计里的历史请求
	// 会失去解释。
	StatusDisabled Status = "disabled"
)

// Declaration 是一个 Adapter 的完整能力声明（PRD §18、REQ-ADAPTER-001）。
//
// Capabilities、AllowedPaths、AllowedMethods、RedactionRules 以 JSON 文本
// 保存：它们的形状随 Adapter 种类变化，拆成列会让每加一种 Adapter 就要迁移一次。
// 结构校验由 REQ-CAP-001 的 JSON Schema 在 transport 层完成。
//
// 声明中不含任何凭据。DefaultRiskLevel 非空是 REQ-ADAPTER-005 AC2
// 「未声明 Risk Level 无法保存」在存储层的实现。
type Declaration struct {
	ID               string
	Service          string
	Kind             Kind
	DisplayName      string
	BaseURL          string
	AuthScheme       AuthScheme
	Capabilities     string
	AllowedPaths     string
	AllowedMethods   string
	RedactionRules   string
	DefaultRiskLevel RiskLabel
	Status           Status
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// DeclarationRepository 读写 Adapter 声明。
//
// 没有删除方法：审计与历史请求会引用服务名，删掉声明等于让账本里的
// 条目无从解释。停用走 SetDeclarationStatus。
type DeclarationRepository interface {
	CreateDeclaration(ctx context.Context, declaration Declaration) (Declaration, error)
	DeclarationByID(ctx context.Context, id string) (Declaration, error)
	// DeclarationByService 是决策链路的入口：拿到服务名后反查它被允许做什么。
	// 查不到即 Adapter 未声明能力，一律拒绝（Fail Closed）。
	DeclarationByService(ctx context.Context, service string) (Declaration, error)
	// EnabledDeclarations 列出参与决策的声明。limit 必须为正。
	EnabledDeclarations(ctx context.Context, limit int) ([]Declaration, error)
	SetDeclarationStatus(ctx context.Context, id string, status Status, at time.Time) (Declaration, error)
	// UpdateDeclaration 替换可变的声明内容。Service 与 Kind 不可改：
	// 它们是这条声明的身份，改了就是另一个 Adapter。
	UpdateDeclaration(ctx context.Context, declaration Declaration) (Declaration, error)
}
