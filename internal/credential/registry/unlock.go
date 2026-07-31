package registry

import (
	"context"
	"sync"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 凭据源锁定时的等待（REQ-GATEWAY-004、PRD §23.2）。
 *
 * 锁着不等于失败。PRD 要求的四件事在这里各有落点：
 *   请求进入等待   → Wait 阻塞而不是立刻返回
 *   提示用户解锁   → Waiting() 让界面知道「有几个请求在等」，提示由界面发出
 *   超时后拒绝     → provider_locked_timeout
 *   不要求 Agent 获取主密码 → 本文件的任何一条路径都不产生、不接受、不提及主密码
 *
 * 最后一条是这里最重要的约束，也是最容易在「友好提示」的名义下被破坏的：
 * 一句「请先解锁 Vault」听起来无害，但 Agent 读到它就会去找解锁的办法，
 * 而那正是威胁模型里不能发生的事。对 Agent 的答复只有码表里的固定文本。
 */

// DefaultUnlockTimeout 是等待解锁的上限（REQ-GATEWAY-004 AC2，假设值）。
const DefaultUnlockTimeout = 2 * time.Minute

// UnlockOptions 是 UnlockWaiter 的配置。
type UnlockOptions struct {
	// Timeout 为零时用 DefaultUnlockTimeout。
	Timeout time.Duration
	// After 产出一个在给定时长后可读的通道，为空时用 time.After。
	//
	// 单独注入而不是走 clock.Clock：那个接口只有 Now，没有定时器，而用 Now
	// 轮询就得靠 sleep 制造时序，而测试规则禁止那样做。
	// 用例注入一个自己控制的通道，超时因此是确定的而不是等出来的。
	After func(time.Duration) <-chan time.Time
}

// UnlockWaiter 让「凭据源锁着」成为一次可被唤醒、可被取消、会超时的等待。
type UnlockWaiter struct {
	mutex   sync.Mutex
	waiters map[chan struct{}]struct{}
	timeout time.Duration
	after   func(time.Duration) <-chan time.Time
}

// NewUnlockWaiter 构造一个等待器。
func NewUnlockWaiter(options UnlockOptions) *UnlockWaiter {
	waiter := &UnlockWaiter{
		waiters: make(map[chan struct{}]struct{}),
		timeout: options.Timeout,
		after:   options.After,
	}
	if waiter.timeout <= 0 {
		waiter.timeout = DefaultUnlockTimeout
	}
	if waiter.after == nil {
		waiter.after = time.After
	}
	return waiter
}

// Waiting 返回此刻有多少个请求正在等待解锁。
//
// 这是「提示用户解锁」的数据来源：界面据此显示缝内侧有请求在等，
// 而不需要任何一个请求把自己的等待状态写进数据库。
func (w *UnlockWaiter) Waiting() int {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return len(w.waiters)
}

// Unlocked 广播「凭据源刚被解锁」，唤醒全部等待者。
//
// 唤醒不等于放行：被唤醒的请求会重新去取一次，取不到照样失败。
func (w *UnlockWaiter) Unlocked() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	for signal := range w.waiters {
		close(signal)
	}
	w.waiters = make(map[chan struct{}]struct{})
}

// Wait 等待解锁，或等到超时。
//
// 解锁返回 nil；超时返回 provider_locked_timeout；ctx 结束时返回
// gateway_unavailable —— 那种情况下请求本身已经没有人在等答复了。
func (w *UnlockWaiter) Wait(ctx context.Context) error {
	signal := w.register()
	defer w.unregister(signal)

	select {
	case <-signal:
		return nil
	case <-w.after(w.timeout):
		return apperr.New(apperr.CodeProviderLockedTimeout).
			WithDetail("等待凭据源解锁超过 " + w.timeout.String())
	case <-ctx.Done():
		return apperr.Wrap(apperr.CodeGatewayUnavailable, ctx.Err()).
			WithDetail("等待解锁期间请求被取消")
	}
}

func (w *UnlockWaiter) register() chan struct{} {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	signal := make(chan struct{})
	w.waiters[signal] = struct{}{}
	return signal
}

// unregister 把等待者摘掉。Unlocked 已经处理过的那些不在表里，不重复关闭。
func (w *UnlockWaiter) unregister(signal chan struct{}) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	delete(w.waiters, signal)
}
