package repo

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Settings 是 settings.Repository 的 SQLite 实现。
type Settings struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ settings.Repository = (*Settings)(nil)

// NewSettings 绑定到已迁移的数据库。
func NewSettings(db *store.DB) *Settings {
	return &Settings{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

// Settings 列出全部偏好，按键名。
//
// 不分页：键名是一张编译期就定死的表（见 platform/settings），
// 行数有上限，无界在这里不是风险。
func (s *Settings) Settings(ctx context.Context) ([]settings.Setting, error) {
	rows, err := s.read.ListSettings(ctx)
	if err != nil {
		return nil, readError(err, "列出偏好失败")
	}

	stored := make([]settings.Setting, 0, len(rows))
	for _, row := range rows {
		setting, convertErr := toSetting(row)
		if convertErr != nil {
			return nil, convertErr
		}
		stored = append(stored, setting)
	}
	return stored, nil
}

// UpsertSetting 写入或覆盖一条偏好。
//
// 先更新后插入而不是反过来：绝大多数写入都是改一条已有的偏好，
// 让常见路径只走一条语句。
func (s *Settings) UpsertSetting(
	ctx context.Context, setting settings.Setting,
) (settings.Setting, error) {
	row, err := s.write.UpdateSettingValue(ctx, queries.UpdateSettingValueParams{
		Value:     setting.Value,
		UpdatedAt: encodeTime(setting.UpdatedAt),
		Name:      setting.Name,
	})
	if err == nil {
		return toSetting(row)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return settings.Setting{}, writeError(err, "更新偏好 "+setting.Name+" 失败")
	}

	created, err := s.write.CreateSetting(ctx, queries.CreateSettingParams{
		ID:        setting.ID,
		Name:      setting.Name,
		Value:     setting.Value,
		CreatedAt: encodeTime(setting.CreatedAt),
		UpdatedAt: encodeTime(setting.UpdatedAt),
	})
	if err != nil {
		return settings.Setting{}, writeError(err, "写入偏好 "+setting.Name+" 失败")
	}
	return toSetting(created)
}

func toSetting(row queries.Setting) (settings.Setting, error) {
	createdAt, err := decodeTime("settings.created_at", row.CreatedAt)
	if err != nil {
		return settings.Setting{}, err
	}
	updatedAt, err := decodeTime("settings.updated_at", row.UpdatedAt)
	if err != nil {
		return settings.Setting{}, err
	}

	return settings.Setting{
		ID:        row.ID,
		Name:      row.Name,
		Value:     row.Value,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}
