package fixtures

import (
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/credential/registry"
)

// ProviderOption 调整凭据来源夹具。
type ProviderOption func(*registry.Provider)

// WithProviderID 换掉来源主键。
func WithProviderID(id string) ProviderOption {
	return func(provider *registry.Provider) { provider.ID = id }
}

// WithProviderKind 换掉来源种类。
func WithProviderKind(kind registry.ProviderKind) ProviderOption {
	return func(provider *registry.Provider) { provider.Kind = kind }
}

// WithProviderLabel 换掉来源名字。
func WithProviderLabel(label string) ProviderOption {
	return func(provider *registry.Provider) { provider.Label = label }
}

// Provider 构造一个合法的凭据来源。
func Provider(options ...ProviderOption) registry.Provider {
	provider := registry.Provider{
		ID:        DefaultProviderID,
		Kind:      registry.KindOnePassword,
		Label:     "个人保险库",
		CreatedAt: Instant,
		UpdatedAt: Instant,
	}
	for _, apply := range options {
		apply(&provider)
	}
	return provider
}

// ReferenceOption 调整凭据引用夹具。
type ReferenceOption func(*registry.Reference)

// WithReferenceID 换掉引用主键。
func WithReferenceID(id string) ReferenceOption {
	return func(reference *registry.Reference) { reference.ID = id }
}

// WithReferenceProviderID 换掉引用指向的来源。
func WithReferenceProviderID(providerID string) ReferenceOption {
	return func(reference *registry.Reference) { reference.ProviderID = providerID }
}

// WithReferenceField 换掉引用的字段名。
func WithReferenceField(field string) ReferenceOption {
	return func(reference *registry.Reference) { reference.Field = field }
}

// WithReferenceHealth 换掉健康状态。
func WithReferenceHealth(status registry.HealthStatus) ReferenceOption {
	return func(reference *registry.Reference) { reference.HealthStatus = status }
}

// WithReferenceMetadata 换掉元数据文本，用于验证 JSON 约束。
func WithReferenceMetadata(metadata string) ReferenceOption {
	return func(reference *registry.Reference) { reference.Metadata = metadata }
}

// Reference 构造一份合法的凭据引用。
//
// 这里的每个字段都是「去哪里取」的坐标，没有一项是凭据本身（REQ-CRED-001）。
// LastVerifiedAt 默认为零值，表示从未验证过。
func Reference(options ...ReferenceOption) registry.Reference {
	reference := registry.Reference{
		ID:           DefaultReferenceID,
		ProviderID:   DefaultProviderID,
		ItemRef:      "op://Personal/GitHub Work",
		Field:        "token",
		Service:      DefaultServiceLabel,
		AccountLabel: "work",
		Metadata:     `{"note":"仅用于工作仓库"}`,
		Capabilities: `["repo.read","repo.write"]`,
		HealthStatus: registry.HealthOK,
		CreatedAt:    Instant,
		UpdatedAt:    Instant,
	}
	for _, apply := range options {
		apply(&reference)
	}
	return reference
}

// IdentityOption 调整身份夹具。
type IdentityOption func(*matcher.Identity)

// WithIdentityID 换掉身份主键。
func WithIdentityID(id string) IdentityOption {
	return func(identity *matcher.Identity) { identity.ID = id }
}

// WithIdentityService 换掉服务名。
func WithIdentityService(service string) IdentityOption {
	return func(identity *matcher.Identity) { identity.Service = service }
}

// WithIdentityAccountLabel 换掉账户名。
func WithIdentityAccountLabel(accountLabel string) IdentityOption {
	return func(identity *matcher.Identity) { identity.AccountLabel = accountLabel }
}

// WithIdentityEnvironment 换掉环境标记。
func WithIdentityEnvironment(environment matcher.Environment) IdentityOption {
	return func(identity *matcher.Identity) { identity.Environment = environment }
}

// WithIdentityCredentialReferenceID 换掉支撑该身份的凭据引用。
func WithIdentityCredentialReferenceID(referenceID string) IdentityOption {
	return func(identity *matcher.Identity) { identity.CredentialReferenceID = referenceID }
}

// Identity 构造一个合法的身份。
func Identity(options ...IdentityOption) matcher.Identity {
	identity := matcher.Identity{
		ID:                    DefaultIdentityID,
		Service:               DefaultServiceLabel,
		AccountLabel:          "work",
		Environment:           matcher.EnvironmentProduction,
		IsDefault:             true,
		Status:                matcher.StatusOK,
		CredentialReferenceID: DefaultReferenceID,
		CreatedAt:             Instant,
		UpdatedAt:             Instant,
	}
	for _, apply := range options {
		apply(&identity)
	}
	return identity
}
