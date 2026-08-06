package registry_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Provider 注册表的行为用例（REQ-CRED-005、REQ-CRED-006）。
 *
 * 用真实的 SQLite 仓储：级联撤销要跨 leases → identities → credential_references
 * 三张表，换成替身测的就是替身之间的接线。
 */

// fakeSource 是一个可控的来源。它取出的是哨兵值 ——
// 真实凭据不允许出现在仓库里的任何地方。
type fakeSource struct {
	kind      registry.ProviderKind
	available error
	fetchErr  error
	// fetchEmpty 让取用成功却返回空值，模拟「条目在、字段是空的」。
	fetchEmpty bool
	fetched    int
}

func (f *fakeSource) Kind() registry.ProviderKind { return f.kind }

func (f *fakeSource) Available(context.Context) error { return f.available }

func (f *fakeSource) Fetch(context.Context, registry.Reference) (secret.Value, error) {
	f.fetched++
	if f.fetchErr != nil {
		return secret.Value{}, f.fetchErr
	}
	if f.fetchEmpty {
		return secret.New(nil), nil
	}
	return secret.New([]byte(sentinel.SentinelToken)), nil
}

type harness struct {
	registry *registry.Registry
	source   *fakeSource
	db       *store.DB
	refs     *repo.CredentialReferences
	leases   *repo.Leases
	clock    *clock.Fixed
}

func newHarness(t *testing.T) harness {
	t.Helper()

	db := fixtures.SeededChain(t)
	fixed := clock.NewFixed(fixtures.Instant)
	refs := repo.NewCredentialReferences(db)
	leases := repo.NewLeases(db)

	built, err := registry.New(registry.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: refs,
		Leases:     leases,
		Clock:      fixed,
	})
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	source := &fakeSource{kind: registry.KindOnePassword}
	if err := built.Register(source); err != nil {
		t.Fatalf("登记来源失败：%v", err)
	}
	return harness{registry: built, source: source, db: db, refs: refs, leases: leases, clock: fixed}
}

func assertCode(t *testing.T, err error, expected apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", expected)
	}
	if !apperr.Is(err, expected) {
		t.Fatalf("错误码为 %s，期望 %s（%v）", apperr.CodeOf(err), expected, err)
	}
}

func markHealthy(t *testing.T, all harness) {
	t.Helper()

	if _, err := all.refs.SetReferenceHealth(t.Context(), fixtures.DefaultReferenceID,
		registry.HealthOK, fixtures.Instant, fixtures.Instant); err != nil {
		t.Fatalf("置为可用失败：%v", err)
	}
}

// ——— 只有三种来源（REQ-CRED-006 AC1）———

func TestImplemented_IsExactlyTheThreeFromThePRD(t *testing.T) {
	expected := []registry.ProviderKind{"1password", "macos-keychain", "local-vault"}

	if got := registry.Implemented(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("实现清单为 %v，期望 %v", got, expected)
	}
}

func TestRegister_UnimplementedKind_IsRefused(t *testing.T) {
	// PRD §9.2 列出的其余五种本期不实现。一个「注册得上但取不到」的来源
	// 只会在运行期才暴露，所以在登记这一步就拒绝。
	all := newHarness(t)

	for _, kind := range []registry.ProviderKind{
		"bitwarden", "vaultwarden", "hashicorp-vault",
		"windows-credential-manager", "environment-import", "",
	} {
		err := all.registry.Register(&fakeSource{kind: kind})
		assertCode(t, err, apperr.CodeInvalidConfiguration)
	}

	if got := all.registry.Kinds(); len(got) != 1 {
		t.Errorf("登记后的种类为 %v，期望只有一种", got)
	}
}

func TestRegister_SameKindTwice_IsRefused(t *testing.T) {
	// 同一种类登记两次会让「这次从哪里取的」答不出来。
	all := newHarness(t)

	err := all.registry.Register(&fakeSource{kind: registry.KindOnePassword})
	assertCode(t, err, apperr.CodeInvalidConfiguration)
}

func TestRegister_NilSource_IsRefused(t *testing.T) {
	all := newHarness(t)

	assertCode(t, all.registry.Register(nil), apperr.CodeInvalidConfiguration)
}

func TestRegister_EveryImplementedKind_IsAccepted(t *testing.T) {
	// 反向对照：三种实现都登记得上，否则上面几条用例换成「什么都拒绝」也通过。
	db := fixtures.SeededChain(t)
	fixed := clock.NewFixed(fixtures.Instant)
	built, err := registry.New(registry.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: repo.NewCredentialReferences(db),
		Leases:     repo.NewLeases(db),
		Clock:      fixed,
	})
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	for _, kind := range registry.Implemented() {
		if err := built.Register(&fakeSource{kind: kind}); err != nil {
			t.Errorf("登记 %s 失败：%v", kind, err)
		}
	}
	if !reflect.DeepEqual(built.Kinds(), registry.Implemented()) {
		t.Errorf("已登记种类为 %v，期望三种都在", built.Kinds())
	}
}

// ——— 取用 ———

func TestFetch_HealthyReference_AsksTheSourceEveryTime(t *testing.T) {
	// 明文不缓存：每次都问来源要。
	all := newHarness(t)
	markHealthy(t, all)

	for round := 1; round <= 3; round++ {
		value, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
		if err != nil {
			t.Fatalf("第 %d 次取用失败：%v", round, err)
		}
		if string(value.Reveal()) != sentinel.SentinelToken {
			t.Errorf("取到的不是来源给的值")
		}
		value.Zero()

		if all.source.fetched != round {
			t.Errorf("第 %d 次取用后来源被问了 %d 次，期望 %d 次",
				round, all.source.fetched, round)
		}
	}
}

func TestFetch_UnhealthyReference_IsRefusedWithoutAskingTheSource(t *testing.T) {
	// needs_reauth 意味着用户还没重新授权，unavailable 意味着上次探测就没通。
	// 两种都不该「先试试看」—— 试探本身可能触发一次用户不知情的解锁提示。
	for _, status := range []registry.HealthStatus{
		registry.HealthNeedsReauth, registry.HealthUnavailable,
	} {
		t.Run(string(status)+" 时拒绝", func(t *testing.T) {
			all := newHarness(t)
			if _, err := all.refs.SetReferenceHealth(t.Context(), fixtures.DefaultReferenceID,
				status, fixtures.Instant, fixtures.Instant); err != nil {
				t.Fatalf("置状态失败：%v", err)
			}

			_, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
			assertCode(t, err, apperr.CodeProviderUnavailable)
			if all.source.fetched != 0 {
				t.Errorf("状态不是 ok 却问了来源 %d 次", all.source.fetched)
			}
		})
	}
}

func TestFetch_UnregisteredSource_IsRefused(t *testing.T) {
	// 来源没登记与取不到凭据，对调用方来说后果一样：请求必须被拒。
	db := fixtures.SeededChain(t)
	fixed := clock.NewFixed(fixtures.Instant)
	refs := repo.NewCredentialReferences(db)
	built, err := registry.New(registry.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: refs,
		Leases:     repo.NewLeases(db),
		Clock:      fixed,
	})
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}
	if _, healthErr := refs.SetReferenceHealth(t.Context(), fixtures.DefaultReferenceID,
		registry.HealthOK, fixtures.Instant, fixtures.Instant); healthErr != nil {
		t.Fatalf("置为可用失败：%v", healthErr)
	}

	_, err = built.Fetch(t.Context(), fixtures.DefaultReferenceID)
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

func TestFetch_WithoutAReference_IsRefused(t *testing.T) {
	all := newHarness(t)

	_, err := all.registry.Fetch(t.Context(), "")
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestFetch_SourceFailure_IsPassedUp(t *testing.T) {
	all := newHarness(t)
	markHealthy(t, all)
	all.source.fetchErr = apperr.New(apperr.CodeProviderLockedTimeout).WithDetail("等待解锁超时")

	_, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID)
	assertCode(t, err, apperr.CodeProviderLockedTimeout)
}

// ——— 健康探测（REQ-CRED-005 AC1）———

func TestProbe_WritesTheResultIntoHealthStatus(t *testing.T) {
	all := newHarness(t)
	markHealthy(t, all)
	all.clock.Advance(time.Hour)

	all.source.available = errors.New("op 不在 PATH 里")
	down, err := all.registry.Probe(t.Context(), fixtures.DefaultReferenceID)
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if down.HealthStatus != registry.HealthUnavailable {
		t.Errorf("状态为 %s，期望 unavailable", down.HealthStatus)
	}
	if !down.LastVerifiedAt.Equal(all.clock.Now()) {
		t.Errorf("验证时刻为 %v，期望 %v", down.LastVerifiedAt, all.clock.Now())
	}

	all.source.available = nil
	up, err := all.registry.Probe(t.Context(), fixtures.DefaultReferenceID)
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if up.HealthStatus != registry.HealthOK {
		t.Errorf("状态为 %s，期望 ok", up.HealthStatus)
	}
	// 两条分支都要刷新验证时刻：只刷一条的话，「60 秒内变过来」
	// 在另一条路径上就不成立了。
	if !up.LastVerifiedAt.Equal(all.clock.Now()) {
		t.Errorf("验证时刻为 %v，期望 %v", up.LastVerifiedAt, all.clock.Now())
	}
}

func TestStale_TurnsTrueWithinSixtySeconds(t *testing.T) {
	// AC1：来源不可用时状态要在 60 秒内变过来。这一层的保证是
	// 「超过 60 秒没验证过的引用会被重新探一次」。
	all := newHarness(t)

	if registry.ProbeInterval != time.Minute {
		t.Errorf("探测间隔为 %v，REQ-CRED-005 AC1 要求 60 秒", registry.ProbeInterval)
	}

	verified := registry.Reference{LastVerifiedAt: fixtures.Instant}
	if all.registry.Stale(verified) {
		t.Error("刚验证过就被当成过期")
	}

	all.clock.Advance(registry.ProbeInterval - time.Second)
	if all.registry.Stale(verified) {
		t.Error("还没到 60 秒就被当成过期")
	}

	all.clock.Advance(time.Second)
	if !all.registry.Stale(verified) {
		t.Error("满 60 秒后仍未被当成过期")
	}

	if !all.registry.Stale(registry.Reference{}) {
		t.Error("从未验证过的引用没被当成过期")
	}
}

// ——— 断开时级联撤销（REQ-CRED-005 AC3）———

func TestDisconnect_RevokesEveryActiveLeaseThatDependsOnTheCredential(t *testing.T) {
	all := newHarness(t)
	markHealthy(t, all)

	leaseManager, err := lease.NewManager(lease.Options{
		Leases: all.leases, Clock: all.clock, IDs: ulid.New(all.clock),
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	issued, err := leaseManager.Issue(t.Context(), lease.IssueRequest{Granted: grantedScope()})
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}

	revoked, err := all.registry.Disconnect(t.Context(), fixtures.DefaultReferenceID)
	if err != nil {
		t.Fatalf("断开失败：%v", err)
	}
	if !reflect.DeepEqual(revoked, []string{issued.ID}) {
		t.Fatalf("撤销了 %v，期望 [%s]", revoked, issued.ID)
	}

	after, err := all.leases.LeaseByID(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if after.Status != lease.StatusRevoked {
		t.Errorf("Lease 状态为 %s，期望 revoked", after.Status)
	}

	reference, err := all.refs.ReferenceByID(t.Context(), fixtures.DefaultReferenceID)
	if err != nil {
		t.Fatalf("读取引用失败：%v", err)
	}
	if reference.HealthStatus != registry.HealthUnavailable {
		t.Errorf("引用状态为 %s，期望 unavailable", reference.HealthStatus)
	}

	// 断开之后取不到凭据了。
	if _, err := all.registry.Fetch(t.Context(), fixtures.DefaultReferenceID); err == nil {
		t.Error("断开之后仍然取得到凭据")
	}
}

func TestDisconnect_UnknownReference_IsRefused(t *testing.T) {
	all := newHarness(t)

	_, err := all.registry.Disconnect(t.Context(), "01K1REFERENCE0000000000NONE")
	assertCode(t, err, apperr.CodeNotFound)
}

func TestNew_MissingAnyDependency_IsRefused(t *testing.T) {
	db := fixtures.MigratedDB(t)
	fixed := clock.NewFixed(fixtures.Instant)
	complete := registry.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: repo.NewCredentialReferences(db),
		Leases:     repo.NewLeases(db),
		Clock:      fixed,
	}
	if _, err := registry.New(complete); err != nil {
		t.Fatalf("完整依赖仍被拒绝：%v", err)
	}

	blanks := []func(*registry.Options){
		func(o *registry.Options) { o.Providers = nil },
		func(o *registry.Options) { o.References = nil },
		func(o *registry.Options) { o.Leases = nil },
		func(o *registry.Options) { o.Clock = nil },
	}
	for index, blank := range blanks {
		options := complete
		blank(&options)
		if _, err := registry.New(options); err == nil {
			t.Errorf("第 %d 项依赖缺失仍构造成功", index+1)
		}
	}
}

func grantedScope() scope.Scope {
	return scope.Scope{
		AgentID:      fixtures.DefaultAgentID,
		WorkspaceID:  fixtures.DefaultWorkspaceID,
		Service:      fixtures.DefaultServiceLabel,
		IdentityID:   fixtures.DefaultIdentityID,
		Account:      "work",
		Resource:     map[string]string{"repo": "Runcoor/opendelo"},
		ResourceKey:  "repo=Runcoor/opendelo",
		Operation:    "pull_request.create",
		NotBefore:    fixtures.Instant,
		ExpiresAt:    fixtures.Instant.Add(scope.DefaultDuration),
		RequestLimit: 1,
		Environment:  "production",
		RiskCeiling:  "medium",
	}
}
