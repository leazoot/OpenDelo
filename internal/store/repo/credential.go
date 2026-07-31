package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// CredentialProviders 是 registry.ProviderRepository 的 SQLite 实现。
type CredentialProviders struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ registry.ProviderRepository = (*CredentialProviders)(nil)

// NewCredentialProviders 绑定到已迁移的数据库。
func NewCredentialProviders(db *store.DB) *CredentialProviders {
	return &CredentialProviders{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (p *CredentialProviders) CreateProvider(
	ctx context.Context, provider registry.Provider,
) (registry.Provider, error) {
	row, err := p.write.CreateCredentialProvider(ctx, queries.CreateCredentialProviderParams{
		ID:        provider.ID,
		Kind:      string(provider.Kind),
		Label:     provider.Label,
		CreatedAt: encodeTime(provider.CreatedAt),
		UpdatedAt: encodeTime(provider.UpdatedAt),
	})
	if err != nil {
		return registry.Provider{}, writeError(err, "写入凭据来源 "+provider.ID+" 失败")
	}
	return toProvider(row)
}

func (p *CredentialProviders) ProviderByID(ctx context.Context, id string) (registry.Provider, error) {
	row, err := p.read.GetCredentialProviderByID(ctx, id)
	if err != nil {
		return registry.Provider{}, readError(err, "读取凭据来源 "+id+" 失败")
	}
	return toProvider(row)
}

func (p *CredentialProviders) ProviderByKindAndLabel(
	ctx context.Context, kind registry.ProviderKind, label string,
) (registry.Provider, error) {
	row, err := p.read.GetCredentialProviderByKindAndLabel(ctx,
		queries.GetCredentialProviderByKindAndLabelParams{Kind: string(kind), Label: label})
	if err != nil {
		return registry.Provider{}, readError(err, "按种类与名字读取凭据来源失败")
	}
	return toProvider(row)
}

func toProvider(row queries.CredentialProvider) (registry.Provider, error) {
	createdAt, err := decodeTime("credential_providers.created_at", row.CreatedAt)
	if err != nil {
		return registry.Provider{}, err
	}
	updatedAt, err := decodeTime("credential_providers.updated_at", row.UpdatedAt)
	if err != nil {
		return registry.Provider{}, err
	}

	return registry.Provider{
		ID:        row.ID,
		Kind:      registry.ProviderKind(row.Kind),
		Label:     row.Label,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

// CredentialReferences 是 registry.ReferenceRepository 的 SQLite 实现。
type CredentialReferences struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ registry.ReferenceRepository = (*CredentialReferences)(nil)

// NewCredentialReferences 绑定到已迁移的数据库。
func NewCredentialReferences(db *store.DB) *CredentialReferences {
	return &CredentialReferences{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (r *CredentialReferences) CreateReference(
	ctx context.Context, reference registry.Reference,
) (registry.Reference, error) {
	row, err := r.write.CreateCredentialReference(ctx, queries.CreateCredentialReferenceParams{
		ID:              reference.ID,
		ProviderID:      reference.ProviderID,
		ProviderItemRef: reference.ItemRef,
		Field:           reference.Field,
		Service:         reference.Service,
		AccountLabel:    reference.AccountLabel,
		Metadata:        reference.Metadata,
		Capabilities:    reference.Capabilities,
		HealthStatus:    string(reference.HealthStatus),
		LastVerifiedAt:  optionalTime(reference.LastVerifiedAt),
		CreatedAt:       encodeTime(reference.CreatedAt),
		UpdatedAt:       encodeTime(reference.UpdatedAt),
	})
	if err != nil {
		return registry.Reference{}, writeError(err, "写入凭据引用 "+reference.ID+" 失败")
	}
	return toReference(row)
}

func (r *CredentialReferences) ReferenceByID(ctx context.Context, id string) (registry.Reference, error) {
	row, err := r.read.GetCredentialReferenceByID(ctx, id)
	if err != nil {
		return registry.Reference{}, readError(err, "读取凭据引用 "+id+" 失败")
	}
	return toReference(row)
}

func (r *CredentialReferences) SetReferenceHealth(
	ctx context.Context, id string, status registry.HealthStatus, verifiedAt, at time.Time,
) (registry.Reference, error) {
	row, err := r.write.UpdateCredentialReferenceHealth(ctx, queries.UpdateCredentialReferenceHealthParams{
		HealthStatus:   string(status),
		LastVerifiedAt: optionalTime(verifiedAt),
		UpdatedAt:      encodeTime(at),
		ID:             id,
	})
	if err != nil {
		return registry.Reference{}, writeError(err, "更新凭据引用 "+id+" 的健康状态失败")
	}
	return toReference(row)
}

func toReference(row queries.CredentialReference) (registry.Reference, error) {
	lastVerifiedAt, err := decodeOptionalTime("credential_references.last_verified_at", row.LastVerifiedAt)
	if err != nil {
		return registry.Reference{}, err
	}
	createdAt, err := decodeTime("credential_references.created_at", row.CreatedAt)
	if err != nil {
		return registry.Reference{}, err
	}
	updatedAt, err := decodeTime("credential_references.updated_at", row.UpdatedAt)
	if err != nil {
		return registry.Reference{}, err
	}

	return registry.Reference{
		ID:             row.ID,
		ProviderID:     row.ProviderID,
		ItemRef:        row.ProviderItemRef,
		Field:          row.Field,
		Service:        row.Service,
		AccountLabel:   row.AccountLabel,
		Metadata:       row.Metadata,
		Capabilities:   row.Capabilities,
		HealthStatus:   registry.HealthStatus(row.HealthStatus),
		LastVerifiedAt: lastVerifiedAt,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}
