package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"
)

/*
 * REQ-NFR-001 的 Ledger 基准：10 万条审计记录 + 过滤下 P95 < 300ms。
 *
 * 与记忆基准同一个理由写成 benchmark（见 trust_bench_test.go 的说明）。
 * 运行方式：go test ./internal/store/ -bench BenchmarkLedger -run '^$'
 */

const (
	benchmarkEventCount = 100_000
	ledgerLatencyBudget = 300 * time.Millisecond
)

func BenchmarkLedgerQuery(b *testing.B) {
	db := benchmarkDB(b)
	seedEventsForBenchmark(b, db)

	ctx := b.Context()
	// Ledger 最常见的一屏：按服务过滤后按时间倒序取 50 条。
	const ledger = `SELECT id, operation_id, event_type, service, operation, outcome, created_at
		FROM audit_events
		WHERE service = ? AND created_at < ?
		ORDER BY created_at DESC
		LIMIT 50`

	samples := make([]time.Duration, 0, b.N)
	for b.Loop() {
		start := time.Now()
		listed, err := countLedgerRows(ctx, db, ledger)
		if err != nil {
			b.Fatalf("Ledger 查询失败：%v", err)
		}
		samples = append(samples, time.Since(start))

		if listed != 50 {
			b.Fatalf("取到 %d 条，期望满页 50 条 —— 这次测的不是真实查询", listed)
		}
	}

	slices.Sort(samples)
	p95 := samples[len(samples)*95/100]
	b.ReportMetric(float64(p95.Nanoseconds())/1e6, "p95_ms")
	if p95 > ledgerLatencyBudget {
		b.Fatalf("%d 条记录下 Ledger 查询 P95 为 %s，预算是 %s", benchmarkEventCount, p95, ledgerLatencyBudget)
	}
}

func countLedgerRows(ctx context.Context, db *DB, statement string) (int, error) {
	rows, err := db.Reader().QueryContext(ctx, statement, "github", "9999-12-31T23:59:59.999Z")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var listed int
	for rows.Next() {
		listed++
	}
	return listed, rows.Err()
}

// seedEventsForBenchmark 写入 benchmarkEventCount 条审计事件。
// 事件分散在多个服务与时刻上，贴近真实账本；不带外键引用，
// 与「认不出请求者也要能记录」是同一种形态。
func seedEventsForBenchmark(b *testing.B, db *DB) {
	b.Helper()

	ctx := b.Context()
	transaction, err := db.Writer().BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("开启事务失败：%v", err)
	}
	defer func() {
		if err := transaction.Rollback(); err != nil && err != sql.ErrTxDone {
			b.Errorf("回滚失败：%v", err)
		}
	}()

	services := []string{"github", "cloudflare", "openai", "internal-api"}
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for index := range benchmarkEventCount {
		moment := base.Add(time.Duration(index) * time.Second).Format(timeLayout)
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO audit_events (
				id, operation_id, event_type, agent_id, device_id, workspace_id,
				identity_id, credential_provider_id, service, operation, resource,
				resolved_scope, verdict, risk_level, decision_id, approval_id,
				lease_id, lease_status, outcome, response_status, duration_ms,
				is_redacted, metadata, created_at)
			 VALUES (?, 'op', 'adapter.executed', NULL, NULL, NULL,
				NULL, NULL, ?, 'repo.read', '{}',
				'{}', 'auto_allow', 'low', NULL, NULL,
				NULL, NULL, 'succeeded', 200, 12,
				1, '{}', ?)`,
			fmt.Sprintf("evt%06d", index), services[index%len(services)], moment); err != nil {
			b.Fatalf("写入第 %d 条基准数据失败：%v", index, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		b.Fatalf("提交失败：%v", err)
	}
}

// timeLayout 与 store/repo 的时间列格式一致：定长 UTC RFC3339 带毫秒，
// 字典序等于时间序。
const timeLayout = "2006-01-02T15:04:05.000Z07:00"
