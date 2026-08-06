package cli

import (
	"context"
	"log/slog"
	"time"
)

/*
 * 到期清扫（REQ-CAP-003）。
 *
 * 「5 分钟没人处理就自动过期」不会自己发生：`approval.Manager.Expire` 要有人叫。
 * 没有这个循环时，一条请求可以在缝前挂几个小时后仍然被批准 —— 而超时存在的
 * 理由正是「过了这么久，当初为什么要做这件事已经没人说得清了」。
 *
 * 用 `time.Ticker` 而不是注入的时钟：这里要的是「隔一会儿再来一次」这个调度，
 * 不是「现在几点」这个判断。到底哪些审批算超时，仍由 `core/approval` 拿注入的
 * 时钟去比对（`.claude/rules/backend.md` §16.1）。
 */

// sweepInterval 是两次清扫之间的间隔。
//
// 比最短的超时（30 秒）略密一档：清扫是轮询，最坏情况下一条审批会比
// 它的时限晚这么久才被关掉，而晚一轮的后果只是它在缝前多留一会儿。
//
// 是变量而不是常量，只为让用例把它调短 —— 它是调度参数，不是决策依据。
var sweepInterval = 15 * time.Second

const (
	// sweepBatch 是一轮最多关闭多少条。
	//
	// 给个上限而不是无界扫描：积压很多时也不该让一轮清扫长时间占住写连接
	// （SQLite 单写者）。剩下的下一轮继续。
	sweepBatch = 100
)

// approvalExpiry 是清扫要用到的那一点点能力。
//
// 定义成接口而不是直接持有 *pipeline.Pipeline：这里只该关闭超时的审批，
// 拿到整条链路意味着这里也能放行。
type approvalExpiry interface {
	ExpireApprovals(ctx context.Context, limit int) (int, error)
}

// sweepApprovals 每隔一段时间关闭一次超时的审批，直到 ctx 结束。
//
// 失败只记日志、不中止循环：清扫是后台工作，一次数据库抖动不该让它从此不再运行。
// 但**绝不静默**——不写日志的话，一个每轮都失败的清扫看起来与「没有超时的审批」
// 完全一样。
func sweepApprovals(ctx context.Context, expiry approvalExpiry, logger *slog.Logger) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			closed, err := expiry.ExpireApprovals(ctx, sweepBatch)
			if err != nil {
				logger.WarnContext(ctx, "关闭超时审批失败", slog.String("error", err.Error()))
				continue
			}
			if closed > 0 {
				logger.InfoContext(ctx, "关闭了等不到人的审批", slog.Int("count", closed))
			}
		}
	}
}
