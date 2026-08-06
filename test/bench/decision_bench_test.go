package bench_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * REQ-NFR-001 第一项：决策链路 P95 < 50ms（不含外部调用与审批等待）。
 *
 * 测的是 Handle 一次完整往返：意图解析 → 身份匹配 → Scope 收敛 → 风险计算 →
 * 决策 → 落审批项 → 写账本。全是真实实现，仓储是临时 SQLite ——
 * 换成替身就只剩函数调用开销，量出来的数不代表用户等的那段时间。
 *
 * 选「要人确认」这一支而不是自动放行，正是因为需求把审批等待排除在外：
 * 到产生审批项为止就是这条链路的终点。
 *
 * 运行方式：go test ./test/bench/ -bench BenchmarkDecisionChain -run '^$'
 */

// decisionLatencyBudget 是决策链路的 P95 上限（REQ-NFR-001）。
const decisionLatencyBudget = 50 * time.Millisecond

// requestChunk 是一次补种的请求行数。
//
// 每次 Handle 都要消费一条 status=received 的请求行（决策与请求是一对一的
// 唯一索引），而 b.Loop 的次数事先不知道，所以按批补。补种不计入样本。
const requestChunk = 512

func BenchmarkDecisionChain(b *testing.B) {
	gateway := fixtures.NewGateway(b)
	catalog := benchmarkCatalog(b)
	ctx := b.Context()

	samples := make([]time.Duration, 0, 1024)
	seeded := 0
	for index := 0; b.Loop(); index++ {
		if index == seeded {
			b.StopTimer()
			seedRequests(b, gateway, seeded, requestChunk)
			seeded += requestChunk
			b.StartTimer()
		}

		inputs := pipeline.Inputs{
			Request:     fixtures.Request(fixtures.WithRequestID(benchmarkRequestID(index))),
			Call:        benchmarkCall(),
			Catalog:     catalog,
			Identities:  []matcher.Identity{fixtures.Identity()},
			AgentTrust:  agentauth.TrustKnown,
			DeviceTrust: agentauth.DeviceTrusted,
		}

		start := time.Now()
		result, err := gateway.Services.Pipeline.Handle(ctx, inputs)
		elapsed := time.Since(start)
		if err != nil {
			b.Fatalf("第 %d 次决策失败：%v", index, err)
		}
		if result.Decision.Verdict != decision.VerdictRequireApproval {
			b.Fatalf("第 %d 次的结论是 %q，期望 require_approval —— 这次测的不是完整链路",
				index, result.Decision.Verdict)
		}
		samples = append(samples, elapsed)
	}

	slices.Sort(samples)
	p95 := samples[len(samples)*95/100]
	b.ReportMetric(float64(p95.Nanoseconds())/1e6, "p95_ms")
	if p95 > decisionLatencyBudget {
		b.Fatalf("决策链路 P95 为 %s，预算是 %s", p95, decisionLatencyBudget)
	}
}

// benchmarkCall 是 fixtures.Request 描述的那次调用：一次中风险写操作。
func benchmarkCall() intent.Call {
	return intent.Call{
		Tool:                "github.pull_request.create",
		Resource:            `{"repo":"Runcoor/opendelo"}`,
		DesiredChange:       `{"base":"main","head":"feature"}`,
		IdentityEnvironment: matcher.EnvironmentProduction,
	}
}

func benchmarkCatalog(b *testing.B) *intent.Catalog {
	b.Helper()

	built, err := intent.NewCatalog([]adapters.Declaration{fixtures.Declaration()})
	if err != nil {
		b.Fatalf("构造能力映射表失败：%v", err)
	}
	return built
}

// benchmarkRequestID 给第 index 次调用一个 26 位的主键，与 ULID 等长。
func benchmarkRequestID(index int) string {
	return fmt.Sprintf("01K1BENCHREQ%014d", index)
}

// seedRequests 补种 count 条待决策的请求行。
func seedRequests(b *testing.B, gateway fixtures.Gateway, from, count int) {
	b.Helper()

	ctx := b.Context()
	for offset := range count {
		request := fixtures.Request(fixtures.WithRequestID(benchmarkRequestID(from + offset)))
		if _, err := gateway.Requests.CreateRequest(ctx, request); err != nil {
			b.Fatalf("补种第 %d 条请求失败：%v", from+offset, err)
		}
	}
}
