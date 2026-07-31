package store_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
)

func openTemp(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), store.FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭数据库失败：%v", err)
		}
	})
	return db
}

func TestOpen_EveryPooledConnection_HasRequiredPragmas(t *testing.T) {
	// AC1。逐个池断言：PRAGMA 的作用域是连接，写池对了不代表读池也对。
	cases := map[string]string{
		"journal_mode":  "wal",
		"foreign_keys":  "1",
		"secure_delete": "1",
		"busy_timeout":  "5000",
		"synchronous":   "1", // NORMAL
	}

	db := openTemp(t)
	pools := map[string]*sql.DB{"写池": db.Writer(), "读池": db.Reader()}

	for poolName, pool := range pools {
		for pragma, want := range cases {
			t.Run(poolName+"/"+pragma, func(t *testing.T) {
				var got string
				if err := pool.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil {
					t.Fatalf("读取 PRAGMA %s 失败：%v", pragma, err)
				}
				if got != want {
					t.Errorf("%s 的 %s = %q，期望 %q", poolName, pragma, got, want)
				}
			})
		}
	}
}

func TestOpen_ForeignKeysEnforced_OnPooledConnections(t *testing.T) {
	// foreign_keys 若只在打开时执行一次，池新建的连接上就是关的。
	// 用一次违反外键的插入证明它在实际使用的连接上生效。
	db := openTemp(t)

	mustExec(t, db.Writer(), `CREATE TABLE parents (id TEXT PRIMARY KEY)`)
	mustExec(t, db.Writer(), `CREATE TABLE children (
		id TEXT PRIMARY KEY,
		parent_id TEXT NOT NULL REFERENCES parents(id) ON DELETE RESTRICT
	)`)

	if _, err := db.Writer().ExecContext(t.Context(),
		`INSERT INTO children (id, parent_id) VALUES ('c1', 'missing')`); err == nil {
		t.Fatal("插入了指向不存在父行的子行，外键约束没有生效")
	}
}

func TestOpen_WritePool_IsLimitedToOneConnection(t *testing.T) {
	// AC3 的前半：写池上限恒为 1，不受 Options 影响。
	db, err := store.Open(t.Context(), store.Options{
		Path:         filepath.Join(t.TempDir(), store.FileName),
		MaxReadConns: 8,
	})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer closeDB(t, db)

	if got := db.Writer().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("写池上限为 %d，期望 1", got)
	}
	if got := db.Reader().Stats().MaxOpenConnections; got != 8 {
		t.Errorf("读池上限为 %d，期望 8", got)
	}
}

func TestOpen_ConcurrentWritesAndReads_DoNotReportDatabaseIsLocked(t *testing.T) {
	// AC3 的后半：并发写不出现 database is locked。
	// 同时开一批读，因为 WAL 下读写并发才是真实场景。
	const writers, readers, perWorker = 8, 4, 25

	db := openTemp(t)
	mustExec(t, db.Writer(), `CREATE TABLE rows_written (id INTEGER PRIMARY KEY AUTOINCREMENT, writer INTEGER NOT NULL)`)

	ctx := t.Context()
	var running sync.WaitGroup
	failures := make(chan error, writers+readers)

	for writer := range writers {
		running.Add(1)
		go func() {
			defer running.Done()
			for range perWorker {
				if _, err := db.Writer().ExecContext(ctx,
					`INSERT INTO rows_written (writer) VALUES (?)`, writer); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	for range readers {
		running.Add(1)
		go func() {
			defer running.Done()
			for range perWorker {
				var counted int
				if err := db.Reader().QueryRowContext(ctx,
					`SELECT COUNT(*) FROM rows_written`).Scan(&counted); err != nil {
					failures <- err
					return
				}
			}
		}()
	}

	running.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("并发读写报错：%v", err)
	}

	var written int
	if err := db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM rows_written`).Scan(&written); err != nil {
		t.Fatalf("统计写入行数失败：%v", err)
	}
	if want := writers * perWorker; written != want {
		t.Errorf("写入了 %d 行，期望 %d 行", written, want)
	}
}

func TestOpen_TransactionsOnWritePool_DoNotLoseUpdates(t *testing.T) {
	// 写池只有一个连接，两个事务不可能同时持有写锁 —— 后来者在池上排队，
	// 而不是在 SQLite 里撞上 SQLITE_BUSY 或覆盖彼此的读-改-写。
	db := openTemp(t)
	mustExec(t, db.Writer(), `CREATE TABLE counters (name TEXT PRIMARY KEY, value INTEGER NOT NULL)`)
	mustExec(t, db.Writer(), `INSERT INTO counters (name, value) VALUES ('leases', 0)`)

	const incrementers, perIncrementer = 6, 20

	ctx := t.Context()
	var running sync.WaitGroup
	failures := make(chan error, incrementers)
	for range incrementers {
		running.Add(1)
		go func() {
			defer running.Done()
			for range perIncrementer {
				if err := incrementInTx(ctx, db.Writer()); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	running.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("事务内递增失败：%v", err)
	}

	var value int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT value FROM counters WHERE name = 'leases'`).Scan(&value); err != nil {
		t.Fatalf("读取计数失败：%v", err)
	}
	if want := incrementers * perIncrementer; value != want {
		t.Errorf("计数为 %d，期望 %d —— 有事务丢失了更新", value, want)
	}
}

func incrementInTx(ctx context.Context, pool *sql.DB) error {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	var value int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = 'leases'`).Scan(&value); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.ExecContext(ctx, `UPDATE counters SET value = ? WHERE name = 'leases'`, value+1); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func TestOpen_NewFile_IsCreatedWith0600(t *testing.T) {
	// AC2 的前半。
	requireUnixPermissions(t)

	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer closeDB(t, db)

	assertPermission(t, path, 0o600)
}

func TestOpen_ExistingFileWithLoosePermissions_IsRejected(t *testing.T) {
	// AC2 的后半：权限被放宽意味着文件曾经暴露过，不自行修好。
	requireUnixPermissions(t)

	for _, permission := range []fs.FileMode{0o644, 0o640, 0o666, 0o604} {
		t.Run(permission.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), store.FileName)
			if err := os.WriteFile(path, nil, permission); err != nil {
				t.Fatalf("准备数据库文件失败：%v", err)
			}

			db, err := store.Open(t.Context(), store.Options{Path: path})
			if err == nil {
				closeDB(t, db)
				t.Fatalf("权限 %s 的数据库文件被接受了", permission)
			}
			assertCode(t, err, apperr.CodeInvalidConfiguration)
		})
	}
}

func TestOpen_PathIsDirectory_IsRejected(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{Path: t.TempDir()})
	if err == nil {
		closeDB(t, db)
		t.Fatal("目录被当成数据库文件接受了")
	}
	assertCode(t, err, apperr.CodeInvalidConfiguration)
}

func TestOpen_EmptyPath_IsRejected(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{})
	if err == nil {
		closeDB(t, db)
		t.Fatal("空路径被接受了")
	}
	assertCode(t, err, apperr.CodeInvalidConfiguration)
}

func TestOpen_MissingParentDirectory_IsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", store.FileName)

	db, err := store.Open(t.Context(), store.Options{Path: path})
	if err == nil {
		closeDB(t, db)
		t.Fatal("父目录不存在时仍然打开成功")
	}
	assertCode(t, err, apperr.CodeInvalidConfiguration)
}

func TestOpen_PathWithSpacesAndHash_IsUsable(t *testing.T) {
	// DSN 是 URL，路径里的空格与 # 必须被转义，否则会被当成查询串或片段。
	dir := filepath.Join(t.TempDir(), "data dir #1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(dir, store.FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer closeDB(t, db)

	mustExec(t, db.Writer(), `CREATE TABLE probes (id TEXT PRIMARY KEY)`)
}

func TestOpen_ReopensExistingDatabase_AndSeesEarlierWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.FileName)

	first, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("首次打开失败：%v", err)
	}
	mustExec(t, first.Writer(), `CREATE TABLE kept (id TEXT PRIMARY KEY)`)
	mustExec(t, first.Writer(), `INSERT INTO kept (id) VALUES ('01J')`)
	if closeErr := first.Close(); closeErr != nil {
		t.Fatalf("关闭失败：%v", closeErr)
	}

	second, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("重新打开失败：%v", err)
	}
	defer closeDB(t, second)

	var id string
	if err := second.Reader().QueryRowContext(t.Context(), `SELECT id FROM kept`).Scan(&id); err != nil {
		t.Fatalf("读取先前写入失败：%v", err)
	}
	if id != "01J" {
		t.Errorf("读到 %q，期望 %q", id, "01J")
	}
}

func TestDB_Path_ReturnsTheOpenedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer closeDB(t, db)

	if db.Path() != path {
		t.Errorf("Path() = %q，期望 %q", db.Path(), path)
	}
}

func TestDB_Close_ClosesBothPools(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), store.FileName)})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}

	if err := db.Writer().PingContext(t.Context()); err == nil {
		t.Error("关闭后写池仍可用")
	}
	if err := db.Reader().PingContext(t.Context()); err == nil {
		t.Error("关闭后读池仍可用")
	}
}

func mustExec(t *testing.T, pool *sql.DB, statement string) {
	t.Helper()

	if _, err := pool.ExecContext(t.Context(), statement); err != nil {
		t.Fatalf("执行 %q 失败：%v", statement, err)
	}
}

func closeDB(t *testing.T, db *store.DB) {
	t.Helper()

	if err := db.Close(); err != nil {
		t.Errorf("关闭数据库失败：%v", err)
	}
}

func assertCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()

	var appError *apperr.Error
	if !errors.As(err, &appError) {
		t.Fatalf("错误不是 *apperr.Error：%v", err)
	}
	if appError.Code() != want {
		t.Errorf("错误码为 %s，期望 %s", appError.Code(), want)
	}
}

func assertPermission(t *testing.T, path string, want fs.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s 的权限是 %s，期望 %s", path, got, want)
	}
}

func requireUnixPermissions(t *testing.T) {
	t.Helper()

	// Windows 的 ACL 与 Unix 权限位语义不同，断言 0600 在那里没有意义。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 Unix 权限位")
	}
}
