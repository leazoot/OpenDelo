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
 * REQ-IDENT-002 的记忆匹配基准：10 万条记忆下 P95 < 10ms。
 *
 * 写成 benchmark 而不是普通用例，是因为准备 10 万条记忆要连带写入
 * 10 万条请求、决策与审批（created_from 是唯一外键），单次约 11 秒。
 * 把它挂在每次 `make check` 上，代价会被付上几百遍。
 *
 * 运行方式：go test ./internal/store/ -bench BenchmarkTrustMemoryMatch -run '^$'
 * 六项 REQ-NFR-001 的可重复基准脚本见测试计划。
 */

const benchmarkMemoryCount = 100_000

// matchLatencyBudget 是这条查询的延迟上限。
const matchLatencyBudget = 10 * time.Millisecond

func BenchmarkTrustMemoryMatch(b *testing.B) {
	db := benchmarkDB(b)
	seedMemoriesForBenchmark(b, db)

	ctx := b.Context()
	const match = `SELECT id FROM trust_memories
		WHERE agent_id = ? AND workspace_id = ? AND service = ? AND status = 'active'
		ORDER BY id LIMIT 50`

	samples := make([]time.Duration, 0, b.N)
	for b.Loop() {
		start := time.Now()
		matched, err := countMatches(ctx, db, match)
		if err != nil {
			b.Fatalf("匹配查询失败：%v", err)
		}
		samples = append(samples, time.Since(start))

		if matched == 0 {
			b.Fatal("基准数据没有被匹配到，这次测的不是真实查询")
		}
	}

	slices.Sort(samples)
	p95 := samples[len(samples)*95/100]
	b.ReportMetric(float64(p95.Nanoseconds())/1e6, "p95_ms")
	if p95 > matchLatencyBudget {
		b.Fatalf("%d 条记忆下匹配查询 P95 为 %s，预算是 %s", benchmarkMemoryCount, p95, matchLatencyBudget)
	}
}

// countMatches 跑一次匹配查询并数出行数。单独成函数是为了让结果集的关闭
// 走 defer（sqlclosecheck），同时把耗时测量留在调用方。
func countMatches(ctx context.Context, db *DB, statement string) (int, error) {
	rows, err := db.Reader().QueryContext(ctx, statement, testAgentID, testWorkspaceID, "github")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var matched int
	for rows.Next() {
		matched++
	}
	return matched, rows.Err()
}

func benchmarkDB(b *testing.B) *DB {
	b.Helper()

	db, err := Open(b.Context(), Options{Path: b.TempDir() + "/" + FileName})
	if err != nil {
		b.Fatalf("打开数据库失败：%v", err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Errorf("关闭数据库失败：%v", err)
		}
	})
	if _, err := Migrate(b.Context(), db); err != nil {
		b.Fatalf("迁移失败：%v", err)
	}
	return db
}

// seedMemoriesForBenchmark 写入 benchmarkMemoryCount 条记忆。
// created_from 是唯一外键，所以每条记忆都要有自己的请求、决策与审批。
func seedMemoriesForBenchmark(b *testing.B, db *DB) {
	b.Helper()

	ctx := b.Context()
	seedBenchmarkRoots(b, db)

	transaction, err := db.Writer().BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("开启事务失败：%v", err)
	}
	defer func() {
		if err := transaction.Rollback(); err != nil && err != sql.ErrTxDone {
			b.Errorf("回滚失败：%v", err)
		}
	}()

	for index := range benchmarkMemoryCount {
		suffix := fmt.Sprintf("%06d", index)
		if err := insertBenchmarkRow(ctx, transaction, suffix); err != nil {
			b.Fatalf("写入第 %d 条基准数据失败：%v", index, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		b.Fatalf("提交失败：%v", err)
	}
}

func insertBenchmarkRow(ctx context.Context, transaction *sql.Tx, suffix string) error {
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO capability_requests (
			id, operation_id, agent_id, workspace_id, service, operation,
			resource, desired_change, reason, status, created_at, updated_at)
		 VALUES (?, 'op', ?, ?, 'github', 'repo.read', '{}', NULL, '基准数据', 'deciding', ?, ?)`,
		"req"+suffix, testAgentID, testWorkspaceID, testInstant, testInstant); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO decisions (
			id, capability_request_id, verdict, risk_level, risk_factors,
			identity_id, match_level, resolved_scope, approval_requirement,
			reason_code, created_at)
		 VALUES (?, ?, 'require_approval', 'low', '[]', NULL, NULL, '{}', 'standard', 'bench', ?)`,
		"dec"+suffix, "req"+suffix, testInstant); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO approvals (
			id, decision_id, action, status, expires_at, decided_at, created_at, updated_at)
		 VALUES (?, ?, NULL, 'pending', ?, NULL, ?, ?)`,
		"apr"+suffix, "dec"+suffix, testLaterTime, testInstant, testInstant); err != nil {
		return err
	}
	_, err := transaction.ExecContext(ctx,
		`INSERT INTO trust_memories (
			id, agent_id, workspace_id, identity_id, service,
			resource_scope, capability_scope, environment, risk_ceiling,
			approval_behavior, created_from, status, invalidation_reason,
			last_used_at, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'github', '{}', '[]', 'production', 'low',
			'auto_allow', ?, 'active', NULL, NULL, ?, ?, ?)`,
		"mem"+suffix, testAgentID, testWorkspaceID, testIdentityID,
		"apr"+suffix, testLaterTime, testInstant, testInstant)
	return err
}

// seedBenchmarkRoots 写入设备、工作区、Agent、凭据链与身份 —— 记忆的其余外键目标。
func seedBenchmarkRoots(b *testing.B, db *DB) {
	b.Helper()

	ctx := b.Context()
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO devices (id, fingerprint, name, trust_status, created_at, updated_at)
		 VALUES (?, 'fp-device', 'MacBook', 'trusted', ?, ?)`,
		testDeviceID, testInstant, testInstant); err != nil {
		b.Fatalf("写入设备失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO workspaces (id, path, project_fingerprint, created_at, updated_at)
		 VALUES (?, '/Users/tester/project', 'fp-project', ?, ?)`,
		testWorkspaceID, testInstant, testInstant); err != nil {
		b.Fatalf("写入工作区失败：%v", err)
	}
	if err := insertAgent(ctx, db, agentValues(testAgentID, "hash-session-key")); err != nil {
		b.Fatalf("写入 Agent 失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO credential_providers (id, kind, label, created_at, updated_at)
		 VALUES (?, '1password', '个人保险库', ?, ?)`,
		testProviderID, testInstant, testInstant); err != nil {
		b.Fatalf("写入凭据来源失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO credential_references (
			id, provider_id, provider_item_ref, field, service, account_label,
			metadata, capabilities, health_status, last_verified_at, created_at, updated_at)
		 VALUES (?, ?, 'op://Personal/GitHub', 'token', 'github', 'work', '{}', '[]', 'ok', NULL, ?, ?)`,
		testReferenceID, testProviderID, testInstant, testInstant); err != nil {
		b.Fatalf("写入凭据引用失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO identities (
			id, service, account_label, environment, is_default, status,
			credential_reference_id, created_at, updated_at)
		 VALUES (?, 'github', 'work', 'production', 1, 'ok', ?, ?, ?)`,
		testIdentityID, testReferenceID, testInstant, testInstant); err != nil {
		b.Fatalf("写入身份失败：%v", err)
	}
}
