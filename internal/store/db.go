package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"runtime"

	_ "modernc.org/sqlite" // 全仓库唯一允许导入 SQLite 驱动的位置，由 test/arch 强制

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

const (
	// FileName 是数据库文件在数据目录下的名字。
	FileName = "opendelo.db"

	// DefaultMaxReadConns 是读连接池的默认上限。WAL 下读不阻塞写，
	// 多开几个读连接可以让 Ledger 查询与决策链路的查询并行。
	DefaultMaxReadConns = 4

	driverName = "sqlite"

	// filePermission 是数据库文件的唯一合法权限。库里有审计与加密后的凭据引用，
	// 同机其他用户不得读取。
	filePermission fs.FileMode = 0o600
)

// connectionPragmas 是数据库规则要求的连接级设置。
//
// 通过 DSN 下发而不是在打开后执行 SQL：foreign_keys 等 PRAGMA 的作用域是单个连接，
// database/sql 的池会在任意时刻新建连接，打开时执行一次根本覆盖不到它们。
var connectionPragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"secure_delete(1)",
	"busy_timeout(5000)",
	"synchronous(NORMAL)",
}

// Options 是 Open 的输入。
type Options struct {
	// Path 是数据库文件路径，必填。其所在目录必须已存在。
	Path string
	// MaxReadConns 是读连接池上限，留空时用 DefaultMaxReadConns。
	MaxReadConns int
}

// DB 是本产品唯一的持久化入口，持有读写分离的两个连接池。
//
// 写池上限恒为 1：SQLite 同时只允许一个写者，在池上限制比让驱动返回
// SQLITE_BUSY 再重试更直接，也让「写操作是串行的」成为可以依赖的性质。
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// Open 打开数据库并建立读写两个连接池。
//
// 文件不存在时创建，权限固定 0600；已存在但权限不符时拒绝打开而不是自行修正 ——
// 权限被改动意味着这个文件曾经暴露过，静默修好会掩盖这件事。
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).WithDetail("数据库路径为空")
	}

	if err := ensureFile(opts.Path); err != nil {
		return nil, err
	}

	maxReadConns := opts.MaxReadConns
	if maxReadConns <= 0 {
		maxReadConns = DefaultMaxReadConns
	}

	writer, err := openPool(ctx, opts.Path, writePool, 1)
	if err != nil {
		return nil, err
	}

	reader, err := openPool(ctx, opts.Path, readPool, maxReadConns)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}

	return &DB{writer: writer, reader: reader, path: opts.Path}, nil
}

// Writer 返回写连接池。全部 INSERT / UPDATE / DELETE 与事务走这里。
func (db *DB) Writer() *sql.DB { return db.writer }

// Reader 返回读连接池。只读查询走这里，避免占用唯一的写连接。
func (db *DB) Reader() *sql.DB { return db.reader }

func (db *DB) Path() string { return db.path }

// Close 关闭两个连接池。两个错误都会被返回，不允许其中一个被吞掉。
func (db *DB) Close() error {
	return errors.Join(db.reader.Close(), db.writer.Close())
}

// poolKind 区分两个池在 DSN 上的差异。
type poolKind int

const (
	readPool poolKind = iota
	writePool
)

func openPool(ctx context.Context, path string, kind poolKind, maxConns int) (*sql.DB, error) {
	pool, err := sql.Open(driverName, dataSourceName(path, kind))
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err).WithDetail("打开数据库失败：" + path)
	}

	pool.SetMaxOpenConns(maxConns)
	pool.SetMaxIdleConns(maxConns)

	// Open 本身是惰性的，不 Ping 就发现不了文件损坏或 PRAGMA 不被接受。
	if err := pool.PingContext(ctx); err != nil {
		return nil, errors.Join(
			apperr.Wrap(apperr.CodeInternal, err).WithDetail("连接数据库失败："+path),
			pool.Close(),
		)
	}
	return pool, nil
}

// dataSourceName 构造 DSN。路径经 url.URL 转义，含空格或 # 的临时目录同样可用。
func dataSourceName(path string, kind poolKind) string {
	query := make(url.Values, len(connectionPragmas)+1)
	for _, pragma := range connectionPragmas {
		query.Add("_pragma", pragma)
	}
	if kind == writePool {
		// 事务一开始就取写锁。默认的 deferred 事务在首次写入时才升级，
		// 升级失败即 SQLITE_BUSY，而那时事务里的读已经发生，只能整体重来。
		query.Set("_txlock", "immediate")
	}

	dsn := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	return dsn.String()
}

// ensureFile 保证数据库文件存在且权限为 0600。
//
// 文件由本包创建而不是交给驱动：驱动建文件时用的是 0644 与 umask 的组合，
// 中间那段时间窗口里文件可被同机其他用户读取。O_EXCL 使并发创建只有一方成功，
// 失败的一方走后面的权限校验。
func ensureFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, filePermission) //nolint:gosec // 路径来自配置，不来自请求
	switch {
	case err == nil:
		if closeErr := file.Close(); closeErr != nil {
			return apperr.Wrap(apperr.CodeInternal, closeErr).WithDetail("无法关闭数据库文件 " + path)
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		return checkFilePermission(path)
	default:
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法创建数据库文件 " + path)
	}
}

func checkFilePermission(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法读取数据库文件 " + path)
	}
	if info.IsDir() {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(path + " 是目录，不是数据库文件")
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志，
	// 断言 0600 在那里没有意义。本期 Windows 仅保证可构建。
	if runtime.GOOS == "windows" {
		return nil
	}

	if permission := info.Mode().Perm(); permission != filePermission {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(
			"数据库文件 " + path + " 的权限是 " + permission.String() + "，必须是 " + filePermission.String(),
		)
	}
	return nil
}
