package repo

import (
	"context"
	"database/sql"
	"errors"
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
	read   *queries.Queries
	write  *queries.Queries
	writer *sql.DB
}

var _ registry.ReferenceRepository = (*CredentialReferences)(nil)

// NewCredentialReferences 绑定到已迁移的数据库。
func NewCredentialReferences(db *store.DB) *CredentialReferences {
	return &CredentialReferences{
		read:   queries.New(db.Reader()),
		write:  queries.New(db.Writer()),
		writer: db.Writer(),
	}
}

// CreateRegistration 在一个事务内登记来源与引用（跨表写入，`database.md` §6.1）。
//
// 两处都是先查后插：来源按（种类，名字）、引用按（来源，条目，字段）。
// 查与插分在两个事务里的话，两个并发的登记会同时查不到再同时插入，
// 后一个撞上唯一索引失败 —— 用户看到的是「连接身份偶尔报错」。
func (r *CredentialReferences) CreateRegistration(
	ctx context.Context, provider registry.Provider, reference registry.Reference,
) (created registry.Reference, err error) {
	transaction, err := r.writer.BeginTx(ctx, nil)
	if err != nil {
		return registry.Reference{}, writeError(err, "开启凭据登记事务失败")
	}
	// 提交之后回滚会返回 ErrTxDone，那是正常路径；其余错误说明事务没能干净收场，
	// 必须并进返回值 —— 悬着的事务会一直占着 SQLite 唯一的写连接。
	defer func() {
		rollbackErr := transaction.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, writeError(rollbackErr, "回滚凭据登记事务失败"))
		}
	}()

	inTransaction := r.write.WithTx(transaction)

	providerID, err := findOrCreateProvider(ctx, inTransaction, provider)
	if err != nil {
		return registry.Reference{}, err
	}
	reference.ProviderID = providerID

	settled, err := findOrCreateReference(ctx, inTransaction, reference)
	if err != nil {
		return registry.Reference{}, err
	}

	if err := transaction.Commit(); err != nil {
		return registry.Reference{}, writeError(err, "提交凭据登记事务失败")
	}
	return settled, nil
}

func findOrCreateProvider(
	ctx context.Context, q *queries.Queries, provider registry.Provider,
) (string, error) {
	existing, err := q.GetCredentialProviderByKindAndLabel(ctx,
		queries.GetCredentialProviderByKindAndLabelParams{
			Kind: string(provider.Kind), Label: provider.Label,
		})
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", readError(err, "按种类与名字读取凭据来源失败")
	}

	row, err := q.CreateCredentialProvider(ctx, queries.CreateCredentialProviderParams{
		ID:        provider.ID,
		Kind:      string(provider.Kind),
		Label:     provider.Label,
		CreatedAt: encodeTime(provider.CreatedAt),
		UpdatedAt: encodeTime(provider.UpdatedAt),
	})
	if err != nil {
		return "", writeError(err, "写入凭据来源 "+provider.ID+" 失败")
	}
	return row.ID, nil
}

func findOrCreateReference(
	ctx context.Context, q *queries.Queries, reference registry.Reference,
) (registry.Reference, error) {
	existing, err := q.GetCredentialReferenceByCoordinates(ctx,
		queries.GetCredentialReferenceByCoordinatesParams{
			ProviderID:      reference.ProviderID,
			ProviderItemRef: reference.ItemRef,
			Field:           reference.Field,
		})
	if err == nil {
		return toReference(existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return registry.Reference{}, readError(err, "按坐标读取凭据引用失败")
	}

	row, err := q.CreateCredentialReference(ctx, createParams(reference))
	if err != nil {
		return registry.Reference{}, writeError(err, "写入凭据引用 "+reference.ID+" 失败")
	}
	return toReference(row)
}

func (r *CredentialReferences) CreateReference(
	ctx context.Context, reference registry.Reference,
) (registry.Reference, error) {
	row, err := r.write.CreateCredentialReference(ctx, createParams(reference))
	if err != nil {
		return registry.Reference{}, writeError(err, "写入凭据引用 "+reference.ID+" 失败")
	}
	return toReference(row)
}

func createParams(reference registry.Reference) queries.CreateCredentialReferenceParams {
	return queries.CreateCredentialReferenceParams{
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
	}
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
