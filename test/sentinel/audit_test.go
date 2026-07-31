package sentinel_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 八个面的哨兵扫描之一：审计（REQ-AUDIT-001 AC2、REQ-NFR-002 AC1）。
 *
 * 这一面扫两处，缺一不可：
 *   - 表里的每一列文本 —— 读回来的内容不含哨兵；
 *   - 数据库文件本身 —— 哨兵没有以任何形式落在磁盘上（WAL 也要算进去）。
 *
 * 第二条是必要的：脱敏若发生在读取时而不是写入时，第一条会通过而第二条不会。
 */

var auditInstant = time.Date(2026, time.July, 28, 9, 15, 30, 123_000_000, time.UTC)

// auditSensitiveKeys 覆盖脱敏词表的每个词以及真实会遇到的写法变体。
var auditSensitiveKeys = []string{
	"authorization", "Authorization", "cookie", "Set-Cookie",
	"token", "access_token", "api_key", "X-API-Key", "apiKey",
	"password", "db_password", "secret", "client_secret",
	"private_key", "PRIVATE_KEY", "credential",
}

type auditFixture struct {
	path     string
	db       *store.DB
	recorder *audit.Recorder
}

func newAuditFixture(t *testing.T) auditFixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败：%v", closeErr)
		}
	})
	if _, migrateErr := store.Migrate(t.Context(), db); migrateErr != nil {
		t.Fatalf("迁移失败：%v", migrateErr)
	}

	recorder, err := audit.NewRecorder(
		repo.NewAuditEvents(db), clock.NewFixed(auditInstant), ulid.New(clock.NewFixed(auditInstant)))
	if err != nil {
		t.Fatalf("组装审计写入器失败：%v", err)
	}
	return auditFixture{path: path, db: db, recorder: recorder}
}

func auditEventWith(metadata, resource string) audit.Event {
	return audit.Event{
		OperationID:   "01K1OPERATION00000000000000",
		Type:          audit.EventAdapterExecuted,
		Service:       "github",
		Operation:     "repo.read",
		Resource:      resource,
		ResolvedScope: `{"service":"github"}`,
		Outcome:       audit.OutcomeSucceeded,
		Metadata:      metadata,
	}
}

func TestAuditFace_SensitiveKeys_NeverReachTheLedger(t *testing.T) {
	ctx := t.Context()
	fixture := newAuditFixture(t)

	for _, key := range auditSensitiveKeys {
		for _, value := range sentinel.All() {
			metadata := `{"` + key + `":"` + value + `"}`
			resource := `{"repo":"opendelo","headers":{"` + key + `":"` + value + `"}}`
			if _, err := fixture.recorder.Record(ctx, auditEventWith(metadata, resource)); err != nil {
				t.Fatalf("写入 %q 的用例失败：%v", key, err)
			}
		}
	}

	assertLedgerHasNoSentinel(t, fixture)
	assertDatabaseFileHasNoSentinel(t, fixture)
}

func TestAuditFace_NestedAndArrayShapes_AreAlsoCovered(t *testing.T) {
	// 凭据藏在数组里的第三层，与藏在顶层没有区别。
	ctx := t.Context()
	fixture := newAuditFixture(t)

	resource := `{"a":{"b":[{"c":{"authorization":"` + sentinel.SentinelToken + `"}}]}}`
	if _, err := fixture.recorder.Record(ctx,
		auditEventWith(`{"note":"ok"}`, resource)); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	assertLedgerHasNoSentinel(t, fixture)
	assertDatabaseFileHasNoSentinel(t, fixture)
}

func TestAuditFace_ReverseControl_ProvesTheScanWorks(t *testing.T) {
	// 反向对照：把哨兵放在一个**不在词表里**的键下，它会如实落库。
	//
	// 这既证明上面的扫描不是永真，也如实说明这一层的边界：审计按 key 脱敏，
	// 拦不住藏在无害键名下的凭据。那一层防线在上游 —— Adapter 的 Redact()
	// （REQ-ADAPTER-007）先过一遍，且凭据明文只以 secret.Value 流转，
	// 而 secret.Value 根本进不到审计的签名里。
	ctx := t.Context()
	fixture := newAuditFixture(t)

	if _, err := fixture.recorder.Record(ctx,
		auditEventWith(`{"note":"`+sentinel.SentinelToken+`"}`, `{"repo":"opendelo"}`)); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	if !ledgerContains(t, fixture, sentinel.SentinelToken) {
		t.Fatal("反向对照没有命中，说明扫描根本没在看这些列")
	}
}

func assertLedgerHasNoSentinel(t *testing.T, fixture auditFixture) {
	t.Helper()

	for _, value := range sentinel.All() {
		if ledgerContains(t, fixture, value) {
			t.Errorf("审计表中出现了哨兵 %s", value)
		}
	}
}

// ledgerContains 逐行逐列扫描审计表的全部文本列。
func ledgerContains(t *testing.T, fixture auditFixture, needle string) bool {
	t.Helper()

	rows, err := fixture.db.Reader().QueryContext(t.Context(),
		`SELECT id, operation_id, event_type, service, operation, resource,
			resolved_scope, outcome, metadata, created_at
		 FROM audit_events`)
	if err != nil {
		t.Fatalf("读取审计表失败：%v", err)
	}
	defer closeAuditRows(t, rows)

	var found bool
	for rows.Next() {
		columns := make([]string, 10)
		targets := make([]any, len(columns))
		for index := range columns {
			targets[index] = &columns[index]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatalf("解析审计行失败：%v", err)
		}
		for _, column := range columns {
			if strings.Contains(column, needle) {
				found = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历审计表失败：%v", err)
	}
	return found
}

// assertDatabaseFileHasNoSentinel 直接读磁盘上的字节。
// 脱敏若发生在读取时而不是写入时，表扫描会通过而这一条不会。
func assertDatabaseFileHasNoSentinel(t *testing.T, fixture auditFixture) {
	t.Helper()

	// WAL 模式下新写入的页可能还在 -wal 文件里，三个文件都要看。
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, err := os.ReadFile(fixture.path + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("读取 %s 失败：%v", fixture.path+suffix, err)
		}
		for _, value := range sentinel.All() {
			if strings.Contains(string(content), value) {
				t.Errorf("数据库文件 %s 中出现了哨兵 %s", filepath.Base(fixture.path+suffix), value)
			}
		}
	}
}

func closeAuditRows(t *testing.T, rows *sql.Rows) {
	t.Helper()

	if err := rows.Close(); err != nil {
		t.Errorf("关闭结果集失败：%v", err)
	}
}
