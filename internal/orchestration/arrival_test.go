package orchestration_test

import (
	"context"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
)

/*
 * 每个面上的请求都要通知到已打开的 Console（回归）。
 *
 * 原先只有 `POST /v1/capability-requests` 那一处广播 arrival，而真实 Agent 走的是
 * MCP 与 Proxy —— 于是缝前来了人，开着的 Console 上什么也不会出现。Gate 的列表
 * 明确不轮询（web/src/data/passages.ts：「列表由 SSE 保持新鲜」），因此那不是
 * 「晚一点显示」，而是**在刷新页面之前永远不显示**。
 *
 * 通知落在 Decide 里，是因为三个面共用的只有这一条决策路径。
 * 由 E2E 的 performance.spec.ts 先撞出来（REQ-NFR-001 第二项根本量不到）。
 */

// recordingArrivals 记下决策结果被通知了几次、通知的是哪一条。
type recordingArrivals struct {
	results []pipeline.Result
}

func (r *recordingArrivals) Announce(_ context.Context, result pipeline.Result) {
	r.results = append(r.results, result)
}

func TestDecide_AnnouncesEveryResultRegardlessOfWhichFaceItCameFrom_Regression(t *testing.T) {
	cases := []struct {
		name          string
		capabilities  string
		operation     string
		desiredChange string
		want          decision.Verdict
	}{
		{
			name:          "要人确认的写操作出现在缝前",
			capabilities:  declaredWrite,
			operation:     "create_issue",
			desiredChange: `{"title":"修一个空指针"}`,
			want:          decision.VerdictRequireApproval,
		},
		{
			// 自动放行的那些同样要通知：缝上画的是「有东西穿过去了」，
			// 只播报被拦下的等于把产品最想让人看见的那一半藏起来。
			name:         "自动放行的读操作也是一次穿越",
			capabilities: declaredRead,
			operation:    "read_repository",
			want:         decision.VerdictAutoAllow,
		},
	}

	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			arrivals := &recordingArrivals{}
			harness := newPreviewHarnessWithArrivals(t, each.capabilities,
				&recordingPreviews{}, arrivals)

			result := harness.submit(t, each.operation, each.desiredChange)

			if result.Decision.Verdict != each.want {
				t.Fatalf("结论是 %q，期望 %q —— 这次跑的不是要测的那条路径",
					result.Decision.Verdict, each.want)
			}
			if len(arrivals.results) != 1 {
				t.Fatalf("通知了 %d 次，期望 1 次 —— 开着的 Console 上看不见这次请求",
					len(arrivals.results))
			}
			if announced := arrivals.results[0]; announced.Request.ID != result.Request.ID {
				t.Errorf("通知出去的是 %q，本次请求是 %q", announced.Request.ID, result.Request.ID)
			}
		})
	}
}
