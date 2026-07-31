package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Identities 是 matcher.IdentityRepository 的 SQLite 实现。
type Identities struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ matcher.IdentityRepository = (*Identities)(nil)

// NewIdentities 绑定到已迁移的数据库。
func NewIdentities(db *store.DB) *Identities {
	return &Identities{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (i *Identities) CreateIdentity(ctx context.Context, identity matcher.Identity) (matcher.Identity, error) {
	row, err := i.write.CreateIdentity(ctx, queries.CreateIdentityParams{
		ID:                    identity.ID,
		Service:               identity.Service,
		AccountLabel:          identity.AccountLabel,
		Environment:           string(identity.Environment),
		IsDefault:             encodeFlag(identity.IsDefault),
		Status:                string(identity.Status),
		CredentialReferenceID: identity.CredentialReferenceID,
		CreatedAt:             encodeTime(identity.CreatedAt),
		UpdatedAt:             encodeTime(identity.UpdatedAt),
	})
	if err != nil {
		return matcher.Identity{}, writeError(err, "写入身份 "+identity.ID+" 失败")
	}
	return toIdentity(row)
}

func (i *Identities) IdentityByID(ctx context.Context, id string) (matcher.Identity, error) {
	row, err := i.read.GetIdentityByID(ctx, id)
	if err != nil {
		return matcher.Identity{}, readError(err, "读取身份 "+id+" 失败")
	}
	return toIdentity(row)
}

func (i *Identities) IdentityByServiceAndAccountLabel(
	ctx context.Context, service, accountLabel string,
) (matcher.Identity, error) {
	row, err := i.read.GetIdentityByServiceAndAccountLabel(ctx,
		queries.GetIdentityByServiceAndAccountLabelParams{Service: service, AccountLabel: accountLabel})
	if err != nil {
		return matcher.Identity{}, readError(err, "按服务与账户名读取身份失败")
	}
	return toIdentity(row)
}

// IdentitiesForService 列出候选身份。limit 必须为正：无界列表查询会随身份数量
// 无限增长。
func (i *Identities) IdentitiesForService(
	ctx context.Context, service string, limit int,
) ([]matcher.Identity, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := i.read.ListIdentitiesByService(ctx,
		queries.ListIdentitiesByServiceParams{Service: service, Limit: int64(limit)})
	if err != nil {
		return nil, readError(err, "列出服务 "+service+" 的身份失败")
	}

	identities := make([]matcher.Identity, 0, len(rows))
	for _, row := range rows {
		identity, convertErr := toIdentity(row)
		if convertErr != nil {
			return nil, convertErr
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

// Identities 列出全部身份，服务 Identities 页面的关系视图。
func (i *Identities) Identities(ctx context.Context, limit int) ([]matcher.Identity, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := i.read.ListIdentities(ctx, int64(limit))
	if err != nil {
		return nil, readError(err, "列出身份失败")
	}

	identities := make([]matcher.Identity, 0, len(rows))
	for _, row := range rows {
		identity, convertErr := toIdentity(row)
		if convertErr != nil {
			return nil, convertErr
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

func (i *Identities) SetIdentityStatus(
	ctx context.Context, id string, status matcher.IdentityStatus, at time.Time,
) (matcher.Identity, error) {
	row, err := i.write.UpdateIdentityStatus(ctx, queries.UpdateIdentityStatusParams{
		Status:    string(status),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if err != nil {
		return matcher.Identity{}, writeError(err, "更新身份 "+id+" 的状态失败")
	}
	return toIdentity(row)
}

func (i *Identities) SetIdentityDefault(
	ctx context.Context, id string, isDefault bool, at time.Time,
) (matcher.Identity, error) {
	row, err := i.write.UpdateIdentityDefault(ctx, queries.UpdateIdentityDefaultParams{
		IsDefault: encodeFlag(isDefault),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if err != nil {
		return matcher.Identity{}, writeError(err, "更新身份 "+id+" 的默认标记失败")
	}
	return toIdentity(row)
}

func toIdentity(row queries.Identity) (matcher.Identity, error) {
	createdAt, err := decodeTime("identities.created_at", row.CreatedAt)
	if err != nil {
		return matcher.Identity{}, err
	}
	updatedAt, err := decodeTime("identities.updated_at", row.UpdatedAt)
	if err != nil {
		return matcher.Identity{}, err
	}
	isDefault, err := decodeFlag("identities.is_default", row.IsDefault)
	if err != nil {
		return matcher.Identity{}, err
	}

	return matcher.Identity{
		ID:                    row.ID,
		Service:               row.Service,
		AccountLabel:          row.AccountLabel,
		Environment:           matcher.Environment(row.Environment),
		IsDefault:             isDefault,
		Status:                matcher.IdentityStatus(row.Status),
		CredentialReferenceID: row.CredentialReferenceID,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}, nil
}
