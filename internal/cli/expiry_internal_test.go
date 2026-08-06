package cli

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

/*
 * 清扫循环（REQ-CAP-003 的接线）。
 *
 * 这条循环此前根本不存在：`approval.Manager.Expire` 与 `ExpireApprovals` 都写好了，
 * 但没有任何东西叫它们。守两件事 —— **它真的会周期性地叫**，
 * 以及**进程要停的时候它自己退出**。后者少了的话，关机时会留下一个还在写
 * 数据库的 goroutine。
 */

// countingExpiry 记下被叫了几次，可以让某几次失败。
type countingExpiry struct {
	mutex  sync.Mutex
	calls  int
	failed int
	fail   bool
	rang   chan struct{}
}

func (c *countingExpiry) ExpireApprovals(ctx context.Context, _ int) (int, error) {
	c.mutex.Lock()
	c.calls++
	shouldFail := c.fail
	if shouldFail {
		c.failed++
	}
	c.mutex.Unlock()

	select {
	case c.rang <- struct{}{}:
	case <-ctx.Done():
	}
	if shouldFail {
		return 0, errors.New("数据库正忙")
	}
	return 1, nil
}

func (c *countingExpiry) counted() (int, int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.calls, c.failed
}

func TestSweepApprovals_KeepsGoingUntilTheContextEnds(t *testing.T) {
	original := sweepIntervalForTest(t, time.Millisecond)
	defer original()

	expiry := &countingExpiry{rang: make(chan struct{}, 8)}
	ctx, stop := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		defer close(done)
		sweepApprovals(ctx, expiry, discardLogger())
	}()

	// 至少跑两轮：只跑一轮的话，一个「叫一次就退出」的实现也能通过。
	for range 2 {
		select {
		case <-expiry.rang:
		case <-time.After(2 * time.Second):
			t.Fatal("清扫没有周期性地运行")
		}
	}

	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 结束之后清扫没有退出 —— 关机时会留下一个还在写数据库的 goroutine")
	}
}

// TestSweepApprovals_AFailedRoundDoesNotStopTheLoop：一次数据库抖动不该让
// 清扫从此不再运行 —— 那会让「超时」再一次静静地失效。
func TestSweepApprovals_AFailedRoundDoesNotStopTheLoop(t *testing.T) {
	original := sweepIntervalForTest(t, time.Millisecond)
	defer original()

	expiry := &countingExpiry{rang: make(chan struct{}, 8), fail: true}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	go sweepApprovals(ctx, expiry, discardLogger())

	for range 3 {
		select {
		case <-expiry.rang:
		case <-time.After(2 * time.Second):
			t.Fatal("失败一次之后清扫停下来了")
		}
	}

	calls, failed := expiry.counted()
	if failed < 3 || calls != failed {
		t.Errorf("叫了 %d 次、失败 %d 次，期望每次都失败且循环没有停", calls, failed)
	}
}

// sweepIntervalForTest 把间隔调短，返回恢复原值的函数。
//
// 用例不能等真实的 15 秒（`.claude/rules/testing.md` §4.6 禁止用 sleep 制造时序），
// 而这个间隔是调度参数、不是决策依据，改它不影响被测行为。
func sweepIntervalForTest(t *testing.T, interval time.Duration) func() {
	t.Helper()

	previous := sweepInterval
	sweepInterval = interval
	return func() { sweepInterval = previous }
}
