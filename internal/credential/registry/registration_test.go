package registry_test

import (
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 凭据登记的行为用例（REQ-CRED-002 AC1）。
 *
 * 登记只写坐标。用例逐条盯着「什么情况下不写」与「已经写过的怎么处理」——
 * 这两问答错都会在界面上留下一个看起来连好、实际取不到凭据的身份。
 */

// spec 拼一份合法的登记输入，用例只写自己关心的差异。
func spec(apply ...func(*registry.Registration)) registry.Registration {
	registration := registry.Registration{
		ProviderID:    "01K1NEWPROVIDER000000000000",
		ReferenceID:   "01K1NEWREFERENCE0000000000",
		Kind:          registry.KindOnePassword,
		ProviderLabel: "个人保险库",
		ItemRef:       "op://Personal/GitHub Bot",
		Field:         "token",
		Service:       fixtures.DefaultServiceLabel,
		AccountLabel:  "bot",
	}
	for _, change := range apply {
		change(&registration)
	}
	return registration
}

func TestRegisterReference_StoresOnlyCoordinates(t *testing.T) {
	all := newHarness(t)

	reference, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("登记失败：%v", err)
	}

	if reference.ItemRef != "op://Personal/GitHub Bot" || reference.Field != "token" {
		t.Errorf("坐标落成了 %q / %q", reference.ItemRef, reference.Field)
	}
	if reference.HealthStatus != registry.HealthOK {
		t.Errorf("健康状态为 %q，刚探通过的引用应为 ok", reference.HealthStatus)
	}
	if reference.LastVerifiedAt.IsZero() {
		t.Error("没有记下验证时刻")
	}
}

// TestRegisterReference_ResolvesTheCoordinatesBeforeWriting_Regression 守的是
// 原始缺陷：登记只探「来源在不在」，从不校验这组坐标指不指得到东西。
//
// 钥匙串的探测是 `security help`，1Password 的是 `op --version` —— 两者都与
// 用户填的条目无关。于是一组永远解析不出东西的坐标也能登记成功，
// 界面上看着是连好的，直到某次审批放行、执行时才失败。
func TestRegisterReference_ResolvesTheCoordinatesBeforeWriting_Regression(t *testing.T) {
	all := newHarness(t)
	// 来源本身可用，但这组坐标取不到东西 —— 正是「条目名写错了」的样子。
	all.source.fetchErr = apperr.New(apperr.CodeCredentialNotAuthorized).
		WithDetail("用例里这组坐标解析不出条目")

	_, err := all.registry.RegisterReference(t.Context(), spec())
	assertCode(t, err, apperr.CodeCredentialNotAuthorized)

	// 一行都没写：坐标改对之后再登记仍然是「新建」，而不是撞上唯一索引。
	all.source.fetchErr = nil
	if _, retryErr := all.registry.RegisterReference(t.Context(), spec()); retryErr != nil {
		t.Fatalf("解析不出那一次留下了痕迹：%v", retryErr)
	}
}

// TestRegisterReference_ZeroesTheProbedPlaintext 守的是校验取到的那份明文。
//
// 校验要真的取一次才算数，而取到的东西必须当场清零：它既不该留在内存里，
// 也不该顺着返回值、错误信息或健康状态漏出去（REQ-CRED-001）。
func TestRegisterReference_ZeroesTheProbedPlaintext(t *testing.T) {
	all := newHarness(t)

	reference, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("登记失败：%v", err)
	}
	if all.source.fetched != 1 {
		t.Errorf("取了 %d 次，期望 1 次 —— 校验要真的解析一次坐标", all.source.fetched)
	}

	// 返回给调用方的引用里没有一处装得下明文，逐字段扫一遍哨兵。
	for name, value := range map[string]string{
		"ItemRef":      reference.ItemRef,
		"Field":        reference.Field,
		"Service":      reference.Service,
		"AccountLabel": reference.AccountLabel,
		"Metadata":     reference.Metadata,
		"Capabilities": reference.Capabilities,
		"HealthStatus": string(reference.HealthStatus),
	} {
		if strings.Contains(value, sentinel.SentinelToken) {
			t.Errorf("引用的 %s 里出现了哨兵", name)
		}
	}
}

// TestRegisterReference_ClassifiesTheFailureAsACoordinateProblem 说明为什么
// 登记期要给取用失败换一个码。
//
// 各来源在取用失败时给的是 provider_unavailable —— 对执行来说那是对的。
// 但登记时来源可用性刚探过，此时再失败最可能是条目名填错了，
// 而「钥匙串锁着」那句提示会让人去解锁一个本来就没锁的钥匙串。
func TestRegisterReference_ClassifiesTheFailureAsACoordinateProblem(t *testing.T) {
	all := newHarness(t)
	all.source.fetchErr = apperr.New(apperr.CodeProviderUnavailable).
		WithDetail("security 命令调用失败")

	_, err := all.registry.RegisterReference(t.Context(), spec())
	assertCode(t, err, apperr.CodeCredentialNotAuthorized)
}

// TestRegisterReference_KeepsTheCodesThatSaySomethingElse：两个确实说的是
// 别的事的错误码不该被归类掉，它们各自有准确的下一步。
func TestRegisterReference_KeepsTheCodesThatSaySomethingElse(t *testing.T) {
	for _, code := range []apperr.Code{apperr.CodeProviderLockedTimeout, apperr.CodeVaultLocked} {
		t.Run(code.String(), func(t *testing.T) {
			all := newHarness(t)
			all.source.fetchErr = apperr.New(code).WithDetail("用例设定")

			_, err := all.registry.RegisterReference(t.Context(), spec())
			assertCode(t, err, code)
		})
	}
}

// TestRegisterReference_AnEmptyCredentialIsRefused：来源答了，但答的是空的。
//
// 空值当成有效凭据登记下去，执行时发出的会是一个不带认证的请求 ——
// 那既不会被这里拦住，也不会在外部服务那里得到一个说得清的错误。
func TestRegisterReference_AnEmptyCredentialIsRefused(t *testing.T) {
	all := newHarness(t)
	all.source.fetchEmpty = true

	_, err := all.registry.RegisterReference(t.Context(), spec())
	assertCode(t, err, apperr.CodeCredentialNotAuthorized)
}

// TestRegisterReference_ProbesBeforeWriting 守的是顺序：来源探不通时一行都不写。
//
// 顺序反过来的话，界面上会出现一个看起来已经连好、实际取不到凭据的身份，
// 那是执行期才会暴露的失败（`.claude/rules/backend.md` §7）。
func TestRegisterReference_ProbesBeforeWriting(t *testing.T) {
	all := newHarness(t)
	all.source.available = apperr.New(apperr.CodeProviderUnavailable).
		WithDetail("用例把来源设为不可用")

	_, err := all.registry.RegisterReference(t.Context(), spec())
	assertCode(t, err, apperr.CodeProviderUnavailable)

	// 引用没写进去：坐标上再登记一次仍然是「新建」，而不是撞上唯一索引。
	all.source.available = nil
	if _, err := all.registry.RegisterReference(t.Context(), spec()); err != nil {
		t.Fatalf("探不通那一次留下了痕迹：%v", err)
	}
}

func TestRegisterReference_UnregisteredKind_IsRefused(t *testing.T) {
	all := newHarness(t)

	_, err := all.registry.RegisterReference(t.Context(),
		spec(func(r *registry.Registration) { r.Kind = registry.KindLocalVault }))
	assertCode(t, err, apperr.CodeProviderUnavailable)
}

// TestRegisterReference_SameCoordinatesTwice_ReusesTheRow 是复用路径：
// 坐标上有唯一索引，第二次登记必须落到复用而不是撞库。
func TestRegisterReference_SameCoordinatesTwice_ReusesTheRow(t *testing.T) {
	all := newHarness(t)

	first, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("第一次登记失败：%v", err)
	}
	second, err := all.registry.RegisterReference(t.Context(),
		spec(func(r *registry.Registration) { r.ReferenceID = "01K1ANOTHERREFERENCE000000" }))
	if err != nil {
		t.Fatalf("第二次登记失败：%v", err)
	}

	if first.ID != second.ID {
		t.Errorf("同一组坐标登记出了两份引用：%q 与 %q", first.ID, second.ID)
	}
}

// TestRegisterReference_RefusesToRelabelTheService 守的是复用带来的风险：
// 坐标相同、服务名不同的请求若照单全收，就等于把一份已登记的 GitHub 凭据
// 改记给另一个服务，之后它会去匹配那个服务的请求。
func TestRegisterReference_RefusesToRelabelTheService(t *testing.T) {
	all := newHarness(t)

	if _, err := all.registry.RegisterReference(t.Context(), spec()); err != nil {
		t.Fatalf("第一次登记失败：%v", err)
	}

	_, err := all.registry.RegisterReference(t.Context(),
		spec(func(r *registry.Registration) { r.Service = "cloudflare" }))
	assertCode(t, err, apperr.CodeConflict)
}

// TestRegisterReference_ReconnectingRestoresHealth 覆盖断开之后再连：
// 断开把引用标成不可用，重连时刚探通过，就该恢复成可用 ——
// 否则连上的是一个立刻会被 Fetch 拒绝的身份。
func TestRegisterReference_ReconnectingRestoresHealth(t *testing.T) {
	all := newHarness(t)

	registered, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("登记失败：%v", err)
	}
	if _, disconnectErr := all.registry.Disconnect(t.Context(), registered.ID); disconnectErr != nil {
		t.Fatalf("断开失败：%v", disconnectErr)
	}

	reconnected, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("重连失败：%v", err)
	}
	if reconnected.HealthStatus != registry.HealthOK {
		t.Errorf("重连后的健康状态为 %q，期望 ok", reconnected.HealthStatus)
	}
}

// TestRegisterReference_AccountLabelIsDescriptiveNotAKey 说明为什么账户名
// 不参与复用时的一致性判断：它只是给人看的名字，改它不影响这份凭据
// 会被匹配到哪里，而服务名会。
func TestRegisterReference_AccountLabelIsDescriptiveNotAKey(t *testing.T) {
	all := newHarness(t)

	first, err := all.registry.RegisterReference(t.Context(), spec())
	if err != nil {
		t.Fatalf("第一次登记失败：%v", err)
	}

	second, err := all.registry.RegisterReference(t.Context(),
		spec(func(r *registry.Registration) { r.AccountLabel = "另一个名字" }))
	if err != nil {
		t.Fatalf("换个账户名就登记不上了：%v", err)
	}
	if first.ID != second.ID {
		t.Error("换个账户名登记出了第二份引用")
	}
	if second.AccountLabel != first.AccountLabel {
		t.Errorf("库里那一行被入参覆盖成了 %q", second.AccountLabel)
	}
}
