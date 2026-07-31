package registry_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 等待解锁的用例（REQ-GATEWAY-004、PRD §23.2）。
 *
 * 超时由注入的通道触发，不靠 sleep 等出来。
 */

// controlledTimeout 造一个由用例决定何时到点的超时源。
//
// requested 由多个等待者并发写入，因此加锁：用例自己的辅助代码在 -race 下
// 出问题会掩盖被测代码的真实行为。
type timeoutProbe struct {
	mutex     sync.Mutex
	requested time.Duration
	fired     chan time.Time
}

func controlledTimeout() *timeoutProbe {
	return &timeoutProbe{fired: make(chan time.Time)}
}

func (p *timeoutProbe) After(timeout time.Duration) <-chan time.Time {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.requested = timeout
	return p.fired
}

func (p *timeoutProbe) Fire() { close(p.fired) }

func (p *timeoutProbe) Requested() time.Duration {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return p.requested
}

func TestUnlockWaiter_Unlocked_WakesTheWaiter(t *testing.T) {
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After})

	done := make(chan error, 1)
	go func() { done <- waiter.Wait(t.Context()) }()

	waitUntilWaiting(t, waiter, 1)
	waiter.Unlocked()

	if err := <-done; err != nil {
		t.Errorf("解锁之后 Wait 返回 %v，期望放行", err)
	}
}

func TestUnlockWaiter_Timeout_DeniesWithProviderLockedTimeout(t *testing.T) {
	// REQ-GATEWAY-004 AC2。
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After})

	done := make(chan error, 1)
	go func() { done <- waiter.Wait(t.Context()) }()

	waitUntilWaiting(t, waiter, 1)
	probe.Fire()

	err := <-done
	if !apperr.Is(err, apperr.CodeProviderLockedTimeout) {
		t.Errorf("超时后返回 %v，期望 provider_locked_timeout", err)
	}
	if probe.Requested() != registry.DefaultUnlockTimeout {
		t.Errorf("等待时长为 %v，期望默认的 %v", probe.Requested(), registry.DefaultUnlockTimeout)
	}
}

func TestUnlockWaiter_ConfiguredTimeout_ReplacesTheDefault(t *testing.T) {
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{
		Timeout: 30 * time.Second, After: probe.After,
	})

	done := make(chan error, 1)
	go func() { done <- waiter.Wait(t.Context()) }()
	waitUntilWaiting(t, waiter, 1)
	probe.Fire()
	<-done

	if probe.Requested() != 30*time.Second {
		t.Errorf("等待时长为 %v，期望配置的 30s", probe.Requested())
	}
}

func TestUnlockWaiter_CancelledRequest_StopsWaiting(t *testing.T) {
	// 请求方已经走了就不必再占着一个 goroutine 等下去。
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- waiter.Wait(ctx) }()

	waitUntilWaiting(t, waiter, 1)
	cancel()

	if err := <-done; err == nil {
		t.Error("请求取消之后 Wait 仍然放行")
	}
}

func TestUnlockWaiter_Waiting_CountsTheRequestsInTheQueue(t *testing.T) {
	// 「提示用户解锁」的数据来源：界面据此知道缝内侧有几个请求在等，
	// 而不需要任何一个请求把等待状态写进数据库。
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After})

	if waiter.Waiting() != 0 {
		t.Fatalf("一开始就有 %d 个等待者", waiter.Waiting())
	}

	results := make([]error, 3)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = waiter.Wait(t.Context())
		}()
	}
	waitUntilWaiting(t, waiter, 3)

	waiter.Unlocked()
	group.Wait()

	// 一次广播唤醒全部等待者，不是只唤醒一个。
	for index, err := range results {
		if err != nil {
			t.Errorf("第 %d 个等待者拿到了 %v，期望被广播唤醒", index, err)
		}
	}

	if waiter.Waiting() != 0 {
		t.Errorf("唤醒之后还剩 %d 个等待者", waiter.Waiting())
	}
}

func TestUnlockWaiter_UnlockedWithNoWaiters_DoesNothing(t *testing.T) {
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{})
	waiter.Unlocked()
	waiter.Unlocked()

	if waiter.Waiting() != 0 {
		t.Errorf("空广播之后有 %d 个等待者", waiter.Waiting())
	}
}

func TestUnlockWaiter_PublicErrors_MentionNoMasterPassword(t *testing.T) {
	// REQ-GATEWAY-004 AC3：返回给 Agent 的错误中不含任何解锁凭据要求或提示词。
	//
	// 禁的是「索取」与「指路」，不是「unlock」这个词本身 —— 码表里那句
	// 「等待凭据源解锁超时，请求已被拒绝」是在陈述一次拒绝，读到它的 Agent
	// 得不到任何可以照做的动作。真正不能出现的是主密码、口令，
	// 以及「请解锁 / 请输入」这类祈使句：那才会让 Agent 去找解锁的办法。
	probe := controlledTimeout()
	waiter := registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After})

	done := make(chan error, 1)
	go func() { done <- waiter.Wait(t.Context()) }()
	waitUntilWaiting(t, waiter, 1)
	probe.Fire()

	public := apperr.PublicOf(<-done, "operation_1")
	forbidden := []string{
		// 凭据本身
		"password", "passphrase", "master key", "secret", "credential of",
		"主密码", "口令", "密码",
		// 祈使句：告诉 Agent 该做什么
		"please ", "enter ", "provide ", "you can ", "try again", "retry",
		"请", "输入", "重试",
	}
	lowered := strings.ToLower(public.Message)
	for _, word := range forbidden {
		if strings.Contains(lowered, strings.ToLower(word)) {
			t.Errorf("对外文本里出现了 %q：%q", word, public.Message)
		}
	}
	if public.OperationID != "operation_1" {
		t.Errorf("operation_id 丢了：%q", public.OperationID)
	}
}

// waitUntilWaiting 等到等待者数量到位。
//
// 轮询而不是 sleep 一个固定时长：这里等的是另一个 goroutine 进入 select，
// 固定时长要么不够要么浪费，且在负载高的机器上必然 flaky。
func waitUntilWaiting(t *testing.T, waiter *registry.UnlockWaiter, want int) {
	t.Helper()

	for range 10000 {
		if waiter.Waiting() >= want {
			return
		}
		sleepOneMillisecond()
	}
	t.Fatalf("等待者数量停在 %d，期望 %d", waiter.Waiting(), want)
}

// sleepOneMillisecond 是轮询之间的让步。
//
// 与测试规则禁止的那种 sleep 不是一回事：那条禁的是
// 「睡够久让某件事发生」，这里睡的是轮询间隔，条件由 Waiting() 判定，
// 且有明确的上限（10000 次即 10 秒）。
func sleepOneMillisecond() { time.Sleep(time.Millisecond) }
