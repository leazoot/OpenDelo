package store

import (
	"context"
	"io/fs"

	"github.com/pressly/goose/v3"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store/migrations"
)

// Migrate 把数据库迁移到最新版本并返回迁移后的版本号。
//
// 已经是最新版本时不做任何事 —— goose 的版本表记录了每个已执行的迁移，
// 重复启动不会重复执行。
func Migrate(ctx context.Context, db *DB) (int64, error) {
	provider, err := newMigrationProvider(db, migrations.FS)
	if err != nil {
		return 0, err
	}

	if _, upErr := provider.Up(ctx); upErr != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, upErr).WithDetail("执行数据库迁移失败")
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, err).WithDetail("读取数据库版本失败")
	}
	return version, nil
}

// rollback 把数据库回滚到 targetVersion（0 表示回到空库）。
//
// 不导出是有意的：回滚会丢数据，属于需要人在场、先备份再执行的操作
// （回滚会丢数据），
// 二进制里不提供这个入口。它存在的意义是让每个迁移的 Down 段真的被执行过 ——
// 写了却从未跑过的 Down 等于没有回滚方案。
func rollback(ctx context.Context, db *DB, targetVersion int64) error {
	provider, err := newMigrationProvider(db, migrations.FS)
	if err != nil {
		return err
	}

	if _, err := provider.DownTo(ctx, targetVersion); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("回滚数据库迁移失败")
	}
	return nil
}

// newMigrationProvider 构造迁移执行器。
//
// 用 Provider 而不是 goose 的包级函数：后者把 dialect、文件系统与日志器存在全局变量里，
// 同一进程内两个数据库就会互相覆盖配置。
//
// 迁移走写池：它只有一条连接，迁移期间不会有第二个写者插进来。
func newMigrationProvider(db *DB, migrationFS fs.FS) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.Writer(), migrationFS,
		// 进度不走 stdout：日志只能从 platform/logging 出去，那里才有脱敏。
		goose.WithVerbose(false),
		// 版本必须连续推进：乱序执行会让两台机器的 schema 相同而版本表不同。
		goose.WithAllowOutofOrder(false),
	)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).WithDetail("初始化迁移执行器失败")
	}
	return provider, nil
}
