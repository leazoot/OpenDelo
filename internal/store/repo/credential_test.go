package repo_test

import (
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// credentialChain 是凭据链上的三个仓储，共用同一个已迁移的数据库。
type credentialChain struct {
	db         *store.DB
	providers  *repo.CredentialProviders
	references *repo.CredentialReferences
	identities *repo.Identities
}

func newCredentialChain(t *testing.T) credentialChain {
	t.Helper()

	db := fixtures.MigratedDB(t)
	return credentialChain{
		db:         db,
		providers:  repo.NewCredentialProviders(db),
		references: repo.NewCredentialReferences(db),
		identities: repo.NewIdentities(db),
	}
}

// seededChain 写好来源与引用后返回三个仓储，供身份用例使用。
func seededChain(t *testing.T) credentialChain {
	t.Helper()

	chain := newCredentialChain(t)
	if _, err := chain.providers.CreateProvider(t.Context(), fixtures.Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	if _, err := chain.references.CreateReference(t.Context(), fixtures.Reference()); err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
	return chain
}

func TestCredentialProviders_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)
	want := fixtures.Provider()

	created, err := chain.providers.CreateProvider(ctx, want)
	if err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.providers.ProviderByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byKind, err := chain.providers.ProviderByKindAndLabel(ctx, want.Kind, want.Label)
	if err != nil {
		t.Fatalf("按种类与名字读取失败：%v", err)
	}
	if byKind != want {
		t.Errorf("按种类与名字读到 %+v，期望 %+v", byKind, want)
	}
}

func TestCredentialProviders_UnimplementedKind_ReportsInvalidRequest(t *testing.T) {
	// REQ-CRED-006：本期不实现的来源存进去也取不出凭据，写入阶段就该拒绝。
	ctx := t.Context()
	chain := newCredentialChain(t)

	_, err := chain.providers.CreateProvider(ctx, fixtures.Provider(fixtures.WithProviderKind("bitwarden")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestCredentialProviders_DuplicateKindAndLabel_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)

	if _, err := chain.providers.CreateProvider(ctx, fixtures.Provider()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}

	_, err := chain.providers.CreateProvider(ctx, fixtures.Provider(
		fixtures.WithProviderID("01K1PROVIDER000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestCredentialReferences_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)
	if _, err := chain.providers.CreateProvider(ctx, fixtures.Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	want := fixtures.Reference()

	created, err := chain.references.CreateReference(ctx, want)
	if err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.references.ReferenceByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestCredentialReferences_NeverVerified_RoundTripsAsZeroTime(t *testing.T) {
	// 从未验证过要落成 NULL 并读回零值。存成一个真实时刻会让「很久没验证了」
	// 这类判断把它当成验证过。
	ctx := t.Context()
	chain := newCredentialChain(t)
	if _, err := chain.providers.CreateProvider(ctx, fixtures.Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}

	created, err := chain.references.CreateReference(ctx, fixtures.Reference())
	if err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
	if !created.LastVerifiedAt.IsZero() {
		t.Errorf("未验证过的引用读回 last_verified_at = %s，期望零值", created.LastVerifiedAt)
	}

	var isNull bool
	if err := chain.db.Reader().QueryRowContext(ctx,
		`SELECT last_verified_at IS NULL FROM credential_references WHERE id = ?`,
		created.ID).Scan(&isNull); err != nil {
		t.Fatalf("读取 last_verified_at 列失败：%v", err)
	}
	if !isNull {
		t.Error("未验证过的引用把 last_verified_at 存成了非 NULL")
	}
}

func TestCredentialReferences_SetHealth_RecordsStatusAndVerificationTime(t *testing.T) {
	// REQ-CRED-005：状态与最近验证时刻是 Identities 页面与 Trust Memory 暂停逻辑的输入。
	ctx := t.Context()
	chain := seededChain(t)

	verifiedAt := fixtures.Instant.Add(time.Hour)
	updated, err := chain.references.SetReferenceHealth(
		ctx, fixtures.DefaultReferenceID, registry.HealthNeedsReauth, verifiedAt, verifiedAt)
	if err != nil {
		t.Fatalf("更新健康状态失败：%v", err)
	}
	if updated.HealthStatus != registry.HealthNeedsReauth {
		t.Errorf("健康状态是 %q，期望 needs_reauth", updated.HealthStatus)
	}
	if !updated.LastVerifiedAt.Equal(verifiedAt) {
		t.Errorf("last_verified_at 是 %s，期望 %s", updated.LastVerifiedAt, verifiedAt)
	}
}

func TestCredentialReferences_UnknownHealthStatus_ReportsInvalidRequest(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)

	_, err := chain.references.SetReferenceHealth(
		ctx, fixtures.DefaultReferenceID, "degraded", fixtures.Instant, fixtures.Instant)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestCredentialReferences_MalformedMetadata_ReportsInvalidRequest(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)
	if _, err := chain.providers.CreateProvider(ctx, fixtures.Provider()); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}

	_, err := chain.references.CreateReference(ctx,
		fixtures.Reference(fixtures.WithReferenceMetadata("not json")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestCredentialReferences_UnknownProvider_ReportsInvalidRequest(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)

	_, err := chain.references.CreateReference(ctx,
		fixtures.Reference(fixtures.WithReferenceProviderID("01K1MISSING000000000000000")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestIdentities_CreateThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)
	want := fixtures.Identity()

	created, err := chain.identities.CreateIdentity(ctx, want)
	if err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if created != want {
		t.Errorf("写入返回 %+v，期望 %+v", created, want)
	}

	byID, err := chain.identities.IdentityByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}

	byLabel, err := chain.identities.IdentityByServiceAndAccountLabel(ctx, want.Service, want.AccountLabel)
	if err != nil {
		t.Fatalf("按服务与账户名读取失败：%v", err)
	}
	if byLabel != want {
		t.Errorf("按服务与账户名读到 %+v，期望 %+v", byLabel, want)
	}
}

func TestIdentities_IsDefaultFalse_RoundTripsAsFalse(t *testing.T) {
	// 布尔在 SQLite 里是 0/1，编解码写反会让每个身份都变成默认身份。
	ctx := t.Context()
	chain := seededChain(t)

	want := fixtures.Identity()
	want.IsDefault = false

	created, err := chain.identities.CreateIdentity(ctx, want)
	if err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if created.IsDefault {
		t.Error("is_default = false 写入后读回来是 true")
	}
}

func TestIdentities_SameServiceDifferentAccounts_AreAllowed(t *testing.T) {
	// REQ-IDENT-001 AC1：同一 service 可以有多个 Identity。
	ctx := t.Context()
	chain := seededChain(t)

	if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity()); err != nil {
		t.Fatalf("写入第一个身份失败：%v", err)
	}
	if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity(
		fixtures.WithIdentityID("01K1IDENTITY000000000000002"),
		fixtures.WithIdentityAccountLabel("personal"),
		fixtures.WithIdentityEnvironment(matcher.EnvironmentNonProduction),
	)); err != nil {
		t.Fatalf("写入第二个身份失败：%v", err)
	}

	candidates, err := chain.identities.IdentitiesForService(ctx, fixtures.DefaultServiceLabel, 10)
	if err != nil {
		t.Fatalf("列出候选身份失败：%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选身份有 %d 个，期望 2 个", len(candidates))
	}
	// ORDER BY account_label：personal 在 work 之前。
	if candidates[0].AccountLabel != "personal" || candidates[1].AccountLabel != "work" {
		t.Errorf("候选顺序是 %q, %q，期望 personal, work",
			candidates[0].AccountLabel, candidates[1].AccountLabel)
	}
}

func TestIdentities_ListAcrossServices_IsOrderedAndBounded(t *testing.T) {
	// 无界列表查询由仓储拒绝：
	// 接入面那一层也会挡，但仓储不能指望调用方都记得挡。
	ctx := t.Context()
	chain := seededChain(t)

	if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity()); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity(
		fixtures.WithIdentityID("01K1IDENTITY000000000000003"),
		fixtures.WithIdentityAccountLabel("personal"),
	)); err != nil {
		t.Fatalf("写入第二个身份失败：%v", err)
	}

	listed, err := chain.identities.Identities(ctx, 10)
	if err != nil {
		t.Fatalf("列出身份失败：%v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("列出了 %d 个身份，期望 2 个", len(listed))
	}
	// ORDER BY service, account_label，与 uq_identities_service_label 的列序一致。
	if listed[0].AccountLabel != "personal" || listed[1].AccountLabel != "work" {
		t.Errorf("顺序是 %q, %q，期望 personal, work",
			listed[0].AccountLabel, listed[1].AccountLabel)
	}

	for _, limit := range []int{0, -1} {
		if _, err = chain.identities.Identities(ctx, limit); err == nil {
			t.Errorf("limit=%d 时仍然返回了结果", limit)
		}
	}
}

func TestIdentities_DuplicateServiceAndAccountLabel_ReportsConflict(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)

	if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity()); err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}

	_, err := chain.identities.CreateIdentity(ctx, fixtures.Identity(
		fixtures.WithIdentityID("01K1IDENTITY000000000000002")))
	assertCode(t, err, apperr.CodeConflict)
}

func TestIdentities_UnknownCredentialReference_ReportsInvalidRequest(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)

	_, err := chain.identities.CreateIdentity(ctx, fixtures.Identity(
		fixtures.WithIdentityCredentialReferenceID("01K1MISSING000000000000000")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestIdentities_ForService_RejectsNonPositiveLimit(t *testing.T) {
	// 无界列表查询会随身份数量无限增长。
	ctx := t.Context()
	chain := seededChain(t)

	for _, limit := range []int{0, -1} {
		_, err := chain.identities.IdentitiesForService(ctx, fixtures.DefaultServiceLabel, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestIdentities_ForService_HonoursTheLimit(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)

	for index, label := range []string{"alpha", "beta", "gamma"} {
		if _, err := chain.identities.CreateIdentity(ctx, fixtures.Identity(
			fixtures.WithIdentityID("01K1IDENTITY00000000000000"+string(rune('A'+index))),
			fixtures.WithIdentityAccountLabel(label),
		)); err != nil {
			t.Fatalf("写入身份 %s 失败：%v", label, err)
		}
	}

	candidates, err := chain.identities.IdentitiesForService(ctx, fixtures.DefaultServiceLabel, 2)
	if err != nil {
		t.Fatalf("列出候选身份失败：%v", err)
	}
	if len(candidates) != 2 {
		t.Errorf("限制为 2 时返回了 %d 个", len(candidates))
	}
}

func TestIdentities_ForService_UnknownService_ReturnsEmpty(t *testing.T) {
	// 查无结果不是错误：匹配链路据此进入「没有可用身份」的分支。
	ctx := t.Context()
	chain := seededChain(t)

	candidates, err := chain.identities.IdentitiesForService(ctx, "cloudflare", 10)
	if err != nil {
		t.Fatalf("列出候选身份失败：%v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("未知服务返回了 %d 个候选", len(candidates))
	}
}

func TestIdentities_SetStatus_MarksTheIdentityForReview(t *testing.T) {
	// REQ-IDENT-004：Scope 变化后转为 needs_review，自动授权暂停。
	ctx := t.Context()
	chain := seededChain(t)
	created, err := chain.identities.CreateIdentity(ctx, fixtures.Identity())
	if err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if created.Status != matcher.StatusOK {
		t.Fatalf("新建身份的状态是 %q，期望 ok", created.Status)
	}

	at := fixtures.Instant.Add(time.Hour)
	updated, err := chain.identities.SetIdentityStatus(ctx, created.ID, matcher.StatusNeedsReview, at)
	if err != nil {
		t.Fatalf("更新状态失败：%v", err)
	}
	if updated.Status != matcher.StatusNeedsReview {
		t.Errorf("状态是 %q，期望 needs_review", updated.Status)
	}
	if !updated.UpdatedAt.Equal(at) {
		t.Errorf("updated_at 是 %s，期望 %s", updated.UpdatedAt, at)
	}
}

func TestIdentities_SetDefault_TogglesTheFlag(t *testing.T) {
	ctx := t.Context()
	chain := seededChain(t)
	created, err := chain.identities.CreateIdentity(ctx, fixtures.Identity())
	if err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}

	updated, err := chain.identities.SetIdentityDefault(ctx, created.ID, false, fixtures.Instant.Add(time.Minute))
	if err != nil {
		t.Fatalf("更新默认标记失败：%v", err)
	}
	if updated.IsDefault {
		t.Error("默认标记被置为 false 后读回来仍是 true")
	}
}

func TestCredentialChain_MissingRows_ReportNotFound(t *testing.T) {
	ctx := t.Context()
	chain := newCredentialChain(t)

	_, err := chain.providers.ProviderByID(ctx, "01K1MISSING000000000000000")
	assertCode(t, err, apperr.CodeNotFound)

	_, err = chain.references.ReferenceByID(ctx, "01K1MISSING000000000000000")
	assertCode(t, err, apperr.CodeNotFound)

	_, err = chain.identities.IdentityByID(ctx, "01K1MISSING000000000000000")
	assertCode(t, err, apperr.CodeNotFound)

	_, err = chain.identities.IdentityByServiceAndAccountLabel(ctx, "github", "unknown")
	assertCode(t, err, apperr.CodeNotFound)
}
