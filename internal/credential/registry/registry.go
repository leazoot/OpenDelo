package registry

import (
	"context"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * Provider 注册表：取用凭据的唯一入口。
 *
 * 三条约束在这里成立：
 *
 *  1. **只有三种来源**（REQ-CRED-006 AC1）。注册表拒绝登记其余五种 ——
 *     它们本期不实现，而一个「注册得上但取不到」的来源只会在运行期才暴露。
 *  2. **明文不缓存**。Fetch 每次都问 Source 要，本包不留任何副本
 *  3. **不确定就拒绝**。健康状态不是 ok、来源探测不通、引用找不到 ——
 *     一律 provider_unavailable，不回退到「先试试看」。
 */

// ProbeInterval 是健康探测的间隔上限。
//
// 取 60 秒是 REQ-CRED-005 AC1 的直接要求：来源不可用时状态要在 60 秒内变过来。
// 超过这个间隔没验证过的引用被 ProbeStale 视为过期，重新探一次。
const ProbeInterval = time.Minute

// Source 是一个凭据来源的实现契约。
//
// 它只回答两件事：现在能不能用，以及这份引用的明文是什么。
// **它不决定用途** —— 这次请求该不该拿到凭据由 core/decision 决定，
// 走到这里时那个问题已经有答案了。
type Source interface {
	// Kind 是这个来源的种类，必须是本期实现的三种之一。
	Kind() ProviderKind
	// Available 报告来源此刻能不能用。返回错误即视为不可用。
	Available(ctx context.Context) error
	// Fetch 取出一份引用的明文。取不到时返回错误，不返回空值 ——
	// 空的 secret.Value 会让调用方以为「这个字段本来就是空的」。
	Fetch(ctx context.Context, reference Reference) (secret.Value, error)
}

// CommandRunner 执行一次外部命令并返回标准输出。
//
// 1Password 与 macOS Keychain 两个来源都靠外部可执行文件取凭据，因此这条契约
// 上收到这里：同一判断出现第二次就上收。
// 抽出它只为一件事：让用例可以在不装 op、不碰用户钥匙串的前提下验证
// 「传了哪些参数」。
type CommandRunner interface {
	Run(ctx context.Context, binary string, args []string) ([]byte, error)
}

// LeaseRevoker 撤销依赖某份凭据引用的全部活跃 Lease（REQ-CRED-005 AC3）。
//
// 接口定义在这里、实现在 store：断开凭据是本包的动作，而「哪些 Lease 依赖它」
// 是一次跨表查询，不该让本包知道表结构（依赖倒置）。
type LeaseRevoker interface {
	// RevokeLeasesByCredentialReference 撤销全部活跃 Lease，返回被撤销的主键。
	RevokeLeasesByCredentialReference(
		ctx context.Context, referenceID string, at time.Time,
	) ([]string, error)
}

// Options 是 Registry 的依赖。
type Options struct {
	Providers  ProviderRepository
	References ReferenceRepository
	Leases     LeaseRevoker
	Clock      clock.Clock
	// Unlock 为空时用默认参数的等待器（REQ-GATEWAY-004）。
	//
	// 没有「不等待、直接失败」的取值：锁着就立刻拒绝会让用户在解锁之后
	// 还得回去让 Agent 重来一次，而 PRD §23.2 要的正是相反的行为。
	Unlock *UnlockWaiter
}

// Registry 管理已登记的来源，并按引用取用凭据。
type Registry struct {
	sources    map[ProviderKind]Source
	providers  ProviderRepository
	references ReferenceRepository
	leases     LeaseRevoker
	clock      clock.Clock
	unlock     *UnlockWaiter
}

// implemented 是本期实现的三种来源（REQ-CRED-006 AC1）。
//
// PRD §9.2 列出的其余五种（Bitwarden、Vaultwarden、HashiCorp Vault、
// Windows Credential Manager、Environment Import）本期不实现，
// 因此这里没有它们的位置 —— 注册表拒绝登记它们，界面也就无从展示。
var implemented = []ProviderKind{KindOnePassword, KindMacOSKeychain, KindLocalVault}

// Implemented 返回本期实现的三种来源，供界面与用例逐条核对。
func Implemented() []ProviderKind {
	return append([]ProviderKind(nil), implemented...)
}

// New 构造注册表。
func New(options Options) (*Registry, error) {
	switch {
	case options.Providers == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Provider 注册表缺少来源仓储")
	case options.References == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Provider 注册表缺少引用仓储")
	case options.Leases == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Provider 注册表缺少 Lease 撤销入口")
	case options.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("Provider 注册表缺少时钟")
	}
	unlock := options.Unlock
	if unlock == nil {
		unlock = NewUnlockWaiter(UnlockOptions{})
	}
	return &Registry{
		sources:    make(map[ProviderKind]Source),
		providers:  options.Providers,
		references: options.References,
		leases:     options.Leases,
		clock:      options.Clock,
		unlock:     unlock,
	}, nil
}

// Register 登记一个来源。
//
// 种类不在本期实现清单里即拒绝；同一种类登记两次也拒绝 ——
// 后者会让「这次从哪里取的」答不出来。
func (r *Registry) Register(source Source) error {
	if source == nil {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail("来源为空")
	}

	kind := source.Kind()
	if !supported(kind) {
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("凭据来源 " + string(kind) + " 本期不实现")
	}
	if _, duplicated := r.sources[kind]; duplicated {
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("凭据来源 " + string(kind) + " 已经登记过")
	}

	r.sources[kind] = source
	return nil
}

// Kinds 返回已登记来源的种类，顺序与 Implemented 一致。
//
// 不按字母排：界面上这三个来源的先后是产品决定的，
// 让它跟着名字走会在改名时悄悄换掉展示顺序。
func (r *Registry) Kinds() []ProviderKind {
	kinds := make([]ProviderKind, 0, len(r.sources))
	for _, kind := range implemented {
		if _, registered := r.sources[kind]; registered {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// Registration 是一次凭据登记的输入：去哪个来源、取哪一项、取哪个字段。
//
// 这里没有一个字段承载凭据明文，也不可能有 —— 明文只在 Fetch 的返回值里
// 以 secret.Value 出现（REQ-CRED-001）。
type Registration struct {
	// ProviderID 与 ReferenceID 是新建时用的主键。复用已有记录时它们被丢弃，
	// 由调用方生成而不是本包生成：生成失败意味着这次操作无法被审计追溯，
	// 那个判断属于调用方
	ProviderID  string
	ReferenceID string

	Kind ProviderKind
	// ProviderLabel 区分同一种类下的多个来源（两个 1Password 账号）。
	ProviderLabel string
	ItemRef       string
	Field         string
	Service       string
	AccountLabel  string
}

// RegisterReference 登记一份凭据引用（REQ-CRED-002 AC1）。
//
// 顺序是先探后写，两道探测都在事务之外完成（事务里不许调用 Provider）：
//
//  1. 来源在不在（Available）
//  2. **这组坐标指不指得到东西**（resolves）
//
// 第二道不能省。各来源的 Available 探的都是「这个来源本身可用吗」——
// 钥匙串跑 `security help`、1Password 跑 `op --version` —— 与用户填的条目无关。
// 只探第一道的话，一组永远解析不出东西的坐标也能登记成功，界面上看着是连好的，
// 直到某次审批放行、执行时才失败，而那时用户已经为它做过一次决定了。
//
// 坐标已经登记过时复用那一行，不再插一份 —— 同一份凭据可以支撑多个身份，
// 断开之后也还能重连。复用时若入参与库里的服务不一致则返回冲突：
// 照单全收等于把一份已登记的 GitHub 凭据改名成 Cloudflare 再拿去匹配。
func (r *Registry) RegisterReference(
	ctx context.Context, spec Registration,
) (Reference, error) {
	source, registered := r.sources[spec.Kind]
	if !registered {
		return Reference{}, unavailable("凭据来源 " + string(spec.Kind) + " 未登记")
	}
	if err := source.Available(ctx); err != nil {
		return Reference{}, err
	}
	if err := resolves(ctx, source, spec); err != nil {
		return Reference{}, err
	}

	now := r.clock.Now()
	settled, err := r.references.CreateRegistration(ctx,
		Provider{
			ID:        spec.ProviderID,
			Kind:      spec.Kind,
			Label:     spec.ProviderLabel,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Reference{
			ID:           spec.ReferenceID,
			ItemRef:      spec.ItemRef,
			Field:        spec.Field,
			Service:      spec.Service,
			AccountLabel: spec.AccountLabel,
			// 元数据与能力声明本期没有录入入口，留空值而不是编一份出来。
			Metadata:       "{}",
			Capabilities:   "[]",
			HealthStatus:   HealthOK,
			LastVerifiedAt: now,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	if err != nil {
		return Reference{}, err
	}

	// 只比服务名。账户名是描述性的，改它不影响这份凭据被匹配到哪里；
	// 服务名不是 —— 把一份已登记的 GitHub 凭据改记成 Cloudflare，
	// 等于让它去匹配另一个服务的请求。
	if settled.Service != spec.Service {
		return Reference{}, apperr.New(apperr.CodeConflict).WithDetail(
			"这组坐标已登记给 " + settled.Service + "，不能改记给 " + spec.Service)
	}

	// 复用的那一行可能是断开时被标成不可用的。刚才已经探通，据此恢复；
	// 新建的那一行本来就是 ok，SetReferenceHealth 写的是同一个值。
	if settled.HealthStatus != HealthOK || settled.LastVerifiedAt.Before(now) {
		return r.references.SetReferenceHealth(ctx, settled.ID, HealthOK, now, now)
	}
	return settled, nil
}

// resolves 校验一组坐标确实指得到一份非空的凭据。
//
// 明文在这里存在的时间就是这个函数的执行时间：取到即 defer Zero，
// 不返回、不记录、不进错误信息 —— 返回的只有「解析得出来吗」这一个布尔含义。
// 这是本包内唯一一处为了校验而取用凭据的地方，因此它不接受调用方传入的
// Reference：能被校验的只有正要登记的那一组坐标。
func resolves(ctx context.Context, source Source, spec Registration) error {
	value, err := source.Fetch(ctx, Reference{
		ItemRef: spec.ItemRef,
		Field:   spec.Field,
		Service: spec.Service,
	})
	if err != nil {
		return asCoordinateFailure(err)
	}
	defer value.Zero()

	// 空值不能当成有效凭据登记下去：执行时发出的会是一个不带认证的请求，
	// 那既不会被这里拦住，也不会在外部服务那里得到一个说得清的错误。
	if value.IsEmpty() {
		return apperr.New(apperr.CodeCredentialNotAuthorized).
			WithDetail("这组坐标指向的字段是空的")
	}
	return nil
}

// asCoordinateFailure 把校验期的取用失败归类到「这组坐标不对」。
//
// 各来源在取用失败时给的是 provider_unavailable —— 对**执行**来说那是对的：
// 条目不存在、用户拒绝授权、钥匙串锁着，后果都是取不到凭据，请求都得拒。
// 但在**登记**这一刻它们不是一回事：来源可用性刚刚才探过，此时再失败，
// 最可能的原因是用户把条目名或账号填错了，而「钥匙串锁着」这句提示
// 会让他去解锁一个本来就没锁的钥匙串。
//
// 两个确实说的是别的事的错误码原样放行：等待解锁超时与保险库锁着，
// 它们各自有准确的下一步。归类只改说法，不改结论 —— 两条路都是拒绝。
func asCoordinateFailure(err error) error {
	if apperr.Is(err, apperr.CodeProviderLockedTimeout) || apperr.Is(err, apperr.CodeVaultLocked) {
		return err
	}
	return apperr.Wrap(apperr.CodeCredentialNotAuthorized, err).
		WithDetail("这组坐标取不到东西")
}

// Fetch 按引用取出凭据明文。
//
// 调用方必须 `defer value.Zero()`：本包不缓存明文，也不知道调用方什么时候用完
//
// 健康状态不是 ok 时直接拒绝，不去试探性地取一次 —— needs_reauth 意味着
// 用户还没重新授权，unavailable 意味着上一次探测就没通。
func (r *Registry) Fetch(ctx context.Context, referenceID string) (secret.Value, error) {
	reference, source, err := r.locate(ctx, referenceID)
	if err != nil {
		return secret.Value{}, err
	}
	if reference.HealthStatus != HealthOK {
		return secret.Value{}, unavailable(
			"凭据引用 " + referenceID + " 的状态为 " + string(reference.HealthStatus))
	}

	value, err := source.Fetch(ctx, reference)
	if err == nil {
		return value, nil
	}
	if !apperr.Is(err, apperr.CodeVaultLocked) {
		return secret.Value{}, err
	}

	// 锁着不是失败，是等待（REQ-GATEWAY-004 AC1）。等到解锁就再取一次；
	// 等到超时就以 provider_locked_timeout 拒绝，不把主密码的事透露给调用方。
	if waitErr := r.unlock.Wait(ctx); waitErr != nil {
		return secret.Value{}, waitErr
	}

	// 被唤醒不等于取得到：解锁之后又被自动锁上，这里如实返回第二次的结果，
	// 而不是把它说成一次超时。
	return source.Fetch(ctx, reference)
}

// Unlocked 广播凭据源已解锁，唤醒正在等待的请求。
//
// 由解锁入口（POST /v1/vault/unlock）在解锁成功后调用。
func (r *Registry) Unlocked() { r.unlock.Unlocked() }

// WaitingForUnlock 返回此刻有多少个请求在等待解锁，供界面提示用户。
func (r *Registry) WaitingForUnlock() int { return r.unlock.Waiting() }

// Probe 验证一份引用背后的来源是否可用，并把结论写进健康状态。
//
// 可用则记 ok 与验证时刻；不可用则记 unavailable。**探测失败不返回错误** ——
// 「这个来源现在用不了」本身就是探测的结论，而不是探测这件事失败了。
func (r *Registry) Probe(ctx context.Context, referenceID string) (Reference, error) {
	_, source, err := r.locate(ctx, referenceID)
	if err != nil {
		return Reference{}, err
	}

	now := r.clock.Now()
	if source.Available(ctx) != nil {
		return r.references.SetReferenceHealth(ctx, referenceID, HealthUnavailable, now, now)
	}
	return r.references.SetReferenceHealth(ctx, referenceID, HealthOK, now, now)
}

// Reference 读取一份凭据引用的元数据，不存在时返回 apperr.CodeNotFound。
//
// 只有元数据：明文的唯一出口是 Fetch，而它的返回类型是 secret.Value，
// 那个类型在本包与 adapter 之外不可见（架构测试强制）。
func (r *Registry) Reference(ctx context.Context, referenceID string) (Reference, error) {
	return r.references.ReferenceByID(ctx, referenceID)
}

// Stale 报告一份引用是否该重新探测了。
//
// 从未验证过的一律算过期：一份没被验证过的引用与一份很久没验证过的引用，
// 在「现在还能不能用」这个问题上是同一种状态。
func (r *Registry) Stale(reference Reference) bool {
	if reference.LastVerifiedAt.IsZero() {
		return true
	}
	return !reference.LastVerifiedAt.After(r.clock.Now().Add(-ProbeInterval))
}

// Disconnect 断开一份凭据引用（REQ-CRED-005 AC3）。
//
// 顺序是先撤 Lease 再改状态：反过来的话，两步之间有一个窗口，
// 状态已经写成不可用、而依赖它的 Lease 还活着，那时正好有请求进来就会被放行。
//
// 撤销失败即返回，状态保持原样 —— 一个「凭据已断开但授权还在」的状态
// 比「断开没成功」危险得多。
//
// 引用不存在时由 SetReferenceHealth 报 not_found，这一层不再先读一次 ——
// 同一个判断有两处答案时，两处迟早会不一致。
func (r *Registry) Disconnect(ctx context.Context, referenceID string) ([]string, error) {
	now := r.clock.Now()
	revoked, err := r.leases.RevokeLeasesByCredentialReference(ctx, referenceID, now)
	if err != nil {
		return nil, err
	}
	if _, err := r.references.SetReferenceHealth(
		ctx, referenceID, HealthUnavailable, now, now,
	); err != nil {
		return revoked, err
	}
	return revoked, nil
}

// locate 找到引用与它对应的来源。
//
// 来源没登记时返回 provider_unavailable 而不是 not_found：对调用方来说
// 这两种情况的后果一样 —— 取不到凭据，请求必须被拒。
func (r *Registry) locate(
	ctx context.Context, referenceID string,
) (Reference, Source, error) {
	if referenceID == "" {
		return Reference{}, nil, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有给出凭据引用")
	}

	reference, err := r.references.ReferenceByID(ctx, referenceID)
	if err != nil {
		return Reference{}, nil, err
	}

	provider, err := r.providers.ProviderByID(ctx, reference.ProviderID)
	if err != nil {
		return Reference{}, nil, err
	}

	source, registered := r.sources[provider.Kind]
	if !registered {
		return Reference{}, nil, unavailable("凭据来源 " + string(provider.Kind) + " 未登记")
	}
	return reference, source, nil
}

func unavailable(detail string) error {
	return apperr.New(apperr.CodeProviderUnavailable).WithDetail(detail)
}

func supported(kind ProviderKind) bool {
	for _, known := range implemented {
		if kind == known {
			return true
		}
	}
	return false
}
