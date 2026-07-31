package registry_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 取用路径上的锁定行为（REQ-GATEWAY-004 AC1/AC2）。
 *
 * 上面的 unlock_test.go 测的是等待器本身；这里测的是**取用真的走了那条等待**——
 * 一个把等待器接错的 Registry，在那边的用例里照样全绿。
 */

// lockingSource 的取用结果可以在等待期间被改掉，用来造出「解锁之后又锁上」。
type lockingSource struct {
	mutex   sync.Mutex
	err     error
	fetched int
}

func (s *lockingSource) Kind() registry.ProviderKind { return registry.KindOnePassword }

func (s *lockingSource) Available(context.Context) error { return nil }

func (s *lockingSource) Fetch(context.Context, registry.Reference) (secret.Value, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.fetched++
	if s.err != nil {
		return secret.Value{}, s.err
	}
	return secret.New([]byte(sentinel.SentinelToken)), nil
}

func (s *lockingSource) setError(err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.err = err
}

func (s *lockingSource) attempts() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.fetched
}

type lockHarness struct {
	registry *registry.Registry
	source   *lockingSource
	probe    *timeoutProbe
}

func newLockHarness(t *testing.T) lockHarness {
	t.Helper()

	db := fixtures.SeededChain(t)
	refs := repo.NewCredentialReferences(db)
	probe := controlledTimeout()

	built, err := registry.New(registry.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: refs,
		Leases:     repo.NewLeases(db),
		Clock:      clock.NewFixed(fixtures.Instant),
		Unlock:     registry.NewUnlockWaiter(registry.UnlockOptions{After: probe.After}),
	})
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	source := &lockingSource{err: apperr.New(apperr.CodeVaultLocked)}
	if err := built.Register(source); err != nil {
		t.Fatalf("登记来源失败：%v", err)
	}
	if _, err := refs.SetReferenceHealth(t.Context(), fixtures.DefaultReferenceID,
		registry.HealthOK, fixtures.Instant, fixtures.Instant); err != nil {
		t.Fatalf("标记引用健康失败：%v", err)
	}
	return lockHarness{registry: built, source: source, probe: probe}
}

func TestFetch_LockedSource_WaitsInsteadOfFailingImmediately(t *testing.T) {
	// REQ-GATEWAY-004 AC1。
	all := newLockHarness(t)

	done := make(chan error, 1)
	go func() {
		value, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
		defer value.Zero()
		done <- err
	}()

	waitUntilQueued(t, all.registry, 1)
	if all.source.attempts() != 1 {
		t.Fatalf("等待期间取用了 %d 次，期望只试过一次", all.source.attempts())
	}

	// 用户解锁：广播之后取用重试一次，这次成功。
	all.source.setError(nil)
	all.registry.Unlocked()

	if err := <-done; err != nil {
		t.Fatalf("解锁之后取用仍然失败：%v", err)
	}
	if all.source.attempts() != 2 {
		t.Errorf("总共取用 %d 次，期望解锁后重试一次", all.source.attempts())
	}
}

func TestFetch_LockStaysOn_DeniesWithProviderLockedTimeout(t *testing.T) {
	// REQ-GATEWAY-004 AC2。超时的是等待，不是外部服务。
	all := newLockHarness(t)

	done := make(chan error, 1)
	go func() {
		_, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
		done <- err
	}()

	waitUntilQueued(t, all.registry, 1)
	all.probe.Fire()

	err := <-done
	if !apperr.Is(err, apperr.CodeProviderLockedTimeout) {
		t.Errorf("超时后返回 %v，期望 provider_locked_timeout", err)
	}
	if all.source.attempts() != 1 {
		t.Errorf("超时路径上取用了 %d 次，期望不再重试", all.source.attempts())
	}
}

func TestFetch_UnlockedThenLockedAgain_ReportsTheSecondResultHonestly(t *testing.T) {
	// 被唤醒不等于取得到。把第二次的结果说成一次超时，账本上就查不出
	// 「解锁之后又被自动锁上」这件事。
	all := newLockHarness(t)

	done := make(chan error, 1)
	go func() {
		_, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
		done <- err
	}()

	waitUntilQueued(t, all.registry, 1)
	all.registry.Unlocked()

	if err := <-done; !apperr.Is(err, apperr.CodeVaultLocked) {
		t.Errorf("第二次仍然锁着时返回 %v，期望如实的 vault_locked", err)
	}
	if all.source.attempts() != 2 {
		t.Errorf("取用了 %d 次，期望唤醒之后重试一次", all.source.attempts())
	}
}

func TestFetch_OtherFailures_DoNotEnterTheUnlockQueue(t *testing.T) {
	// 反向对照：只有「锁着」才等。凭据源本身不可用时等下去毫无意义 ——
	// 没有任何用户操作能结束那个等待。
	all := newLockHarness(t)
	all.source.setError(apperr.New(apperr.CodeProviderUnavailable))

	_, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)

	if !apperr.Is(err, apperr.CodeProviderUnavailable) {
		t.Errorf("返回 %v，期望原样上抛 provider_unavailable", err)
	}
	if all.registry.WaitingForUnlock() != 0 {
		t.Errorf("有 %d 个请求进了解锁队列", all.registry.WaitingForUnlock())
	}
	if all.source.attempts() != 1 {
		t.Errorf("取用了 %d 次，期望只试一次", all.source.attempts())
	}
}

func TestFetch_Succeeds_WithoutTouchingTheUnlockQueue(t *testing.T) {
	// 反向对照：没有这条，上面几条可以靠「取用永远失败」通过。
	all := newLockHarness(t)
	all.source.setError(nil)

	value, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
	defer value.Zero()

	if err != nil {
		t.Fatalf("取用失败：%v", err)
	}
	if all.registry.WaitingForUnlock() != 0 {
		t.Errorf("成功路径上却有 %d 个请求在等解锁", all.registry.WaitingForUnlock())
	}
}

// waitUntilQueued 等到解锁队列里出现指定数量的请求。
func waitUntilQueued(t *testing.T, built *registry.Registry, want int) {
	t.Helper()

	for range 10000 {
		if built.WaitingForUnlock() >= want {
			return
		}
		sleepOneMillisecond()
	}
	t.Fatalf("解锁队列停在 %d，期望 %d", built.WaitingForUnlock(), want)
}
