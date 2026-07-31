package repo

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// ServiceAdapters 是 registry.DeclarationRepository 的 SQLite 实现。
type ServiceAdapters struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ registry.DeclarationRepository = (*ServiceAdapters)(nil)

// NewServiceAdapters 绑定到已迁移的数据库。
func NewServiceAdapters(db *store.DB) *ServiceAdapters {
	return &ServiceAdapters{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (s *ServiceAdapters) CreateDeclaration(
	ctx context.Context, declaration registry.Declaration,
) (registry.Declaration, error) {
	row, err := s.write.CreateServiceAdapter(ctx, queries.CreateServiceAdapterParams{
		ID:               declaration.ID,
		Service:          declaration.Service,
		Kind:             string(declaration.Kind),
		DisplayName:      declaration.DisplayName,
		BaseUrl:          declaration.BaseURL,
		AuthScheme:       string(declaration.AuthScheme),
		Capabilities:     declaration.Capabilities,
		AllowedPaths:     declaration.AllowedPaths,
		AllowedMethods:   declaration.AllowedMethods,
		RedactionRules:   declaration.RedactionRules,
		DefaultRiskLevel: string(declaration.DefaultRiskLevel),
		Status:           string(declaration.Status),
		CreatedAt:        encodeTime(declaration.CreatedAt),
		UpdatedAt:        encodeTime(declaration.UpdatedAt),
	})
	if err != nil {
		return registry.Declaration{}, writeError(err, "写入 Adapter 声明 "+declaration.ID+" 失败")
	}
	return toDeclaration(row)
}

func (s *ServiceAdapters) DeclarationByID(ctx context.Context, id string) (registry.Declaration, error) {
	row, err := s.read.GetServiceAdapterByID(ctx, id)
	if err != nil {
		return registry.Declaration{}, readError(err, "读取 Adapter 声明 "+id+" 失败")
	}
	return toDeclaration(row)
}

func (s *ServiceAdapters) DeclarationByService(
	ctx context.Context, service string,
) (registry.Declaration, error) {
	row, err := s.read.GetServiceAdapterByService(ctx, service)
	if err != nil {
		return registry.Declaration{}, readError(err, "读取服务 "+service+" 的 Adapter 声明失败")
	}
	return toDeclaration(row)
}

func (s *ServiceAdapters) EnabledDeclarations(
	ctx context.Context, limit int,
) ([]registry.Declaration, error) {
	if limit <= 0 {
		return nil, invalidLimit(limit)
	}

	rows, err := s.read.ListEnabledServiceAdapters(ctx, int64(limit))
	if err != nil {
		return nil, readError(err, "列出启用中的 Adapter 声明失败")
	}

	declarations := make([]registry.Declaration, 0, len(rows))
	for _, row := range rows {
		declaration, convertErr := toDeclaration(row)
		if convertErr != nil {
			return nil, convertErr
		}
		declarations = append(declarations, declaration)
	}
	return declarations, nil
}

func (s *ServiceAdapters) SetDeclarationStatus(
	ctx context.Context, id string, status registry.Status, at time.Time,
) (registry.Declaration, error) {
	row, err := s.write.UpdateServiceAdapterStatus(ctx, queries.UpdateServiceAdapterStatusParams{
		Status:    string(status),
		UpdatedAt: encodeTime(at),
		ID:        id,
	})
	if err != nil {
		return registry.Declaration{}, writeError(err, "更新 Adapter 声明 "+id+" 的状态失败")
	}
	return toDeclaration(row)
}

// UpdateDeclaration 只替换声明内容。Service 与 Kind 不在 SET 列表里：
// 它们是这条声明的身份，改了就是另一个 Adapter，而历史请求还指着旧的服务名。
func (s *ServiceAdapters) UpdateDeclaration(
	ctx context.Context, declaration registry.Declaration,
) (registry.Declaration, error) {
	row, err := s.write.UpdateServiceAdapterDeclaration(ctx,
		queries.UpdateServiceAdapterDeclarationParams{
			BaseUrl:          declaration.BaseURL,
			AuthScheme:       string(declaration.AuthScheme),
			Capabilities:     declaration.Capabilities,
			AllowedPaths:     declaration.AllowedPaths,
			AllowedMethods:   declaration.AllowedMethods,
			RedactionRules:   declaration.RedactionRules,
			DefaultRiskLevel: string(declaration.DefaultRiskLevel),
			UpdatedAt:        encodeTime(declaration.UpdatedAt),
			ID:               declaration.ID,
		})
	if err != nil {
		return registry.Declaration{}, writeError(err, "更新 Adapter 声明 "+declaration.ID+" 失败")
	}
	return toDeclaration(row)
}

func toDeclaration(row queries.ServiceAdapter) (registry.Declaration, error) {
	createdAt, err := decodeTime("service_adapters.created_at", row.CreatedAt)
	if err != nil {
		return registry.Declaration{}, err
	}
	updatedAt, err := decodeTime("service_adapters.updated_at", row.UpdatedAt)
	if err != nil {
		return registry.Declaration{}, err
	}

	return registry.Declaration{
		ID:               row.ID,
		Service:          row.Service,
		Kind:             registry.Kind(row.Kind),
		DisplayName:      row.DisplayName,
		BaseURL:          row.BaseUrl,
		AuthScheme:       registry.AuthScheme(row.AuthScheme),
		Capabilities:     row.Capabilities,
		AllowedPaths:     row.AllowedPaths,
		AllowedMethods:   row.AllowedMethods,
		RedactionRules:   row.RedactionRules,
		DefaultRiskLevel: registry.RiskLabel(row.DefaultRiskLevel),
		Status:           registry.Status(row.Status),
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}
