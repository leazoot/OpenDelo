package store

import (
	"context"
	"strings"
	"testing"
)

/*
 * credential_providers / credential_references / identities 三张表的约束检查。
 */

const (
	testProviderID  = "01K1PROVIDER000000000000000"
	testReferenceID = "01K1REFERENCE00000000000000"
	testIdentityID  = "01K1IDENTITY000000000000000"
)

// secretColumnWords 是「这一列可能装着凭据明文」的信号词，取自
// 安全规则里的脱敏词表。
//
// 三张表的列名都不得命中：命中意味着有人在 schema 里开了一条存明文的口子，
// 而 REQ-CRED-001 AC1 要求这条口子根本不存在。
var secretColumnWords = []string{
	"token", "secret", "password", "passphrase", "apikey", "api_key",
	"private_key", "credential_value", "plaintext", "cipher",
}

func insertProvider(ctx context.Context, db *DB, id, kind, label string) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO credential_providers (id, kind, label, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, kind, label, testInstant, testInstant)
	return err
}

func insertReference(ctx context.Context, db *DB, id, providerID, field, health, metadata string) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO credential_references (
			id, provider_id, provider_item_ref, field, service, account_label,
			metadata, capabilities, health_status, last_verified_at, created_at, updated_at)
		 VALUES (?, ?, 'op://Personal/GitHub', ?, 'github', 'work', ?, '[]', ?, NULL, ?, ?)`,
		id, providerID, field, metadata, health, testInstant, testInstant)
	return err
}

func insertIdentity(ctx context.Context, db *DB, id, service, accountLabel, environment string, isDefault int) error {
	_, err := db.Writer().ExecContext(ctx,
		`INSERT INTO identities (
			id, service, account_label, environment, is_default, status,
			credential_reference_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'ok', ?, ?, ?)`,
		id, service, accountLabel, environment, isDefault, testReferenceID, testInstant, testInstant)
	return err
}

// seedCredentialChain 准备 provider → reference 这条外键链。
func seedCredentialChain(t *testing.T, db *DB) {
	t.Helper()

	ctx := t.Context()
	if err := insertProvider(ctx, db, testProviderID, "1password", "个人保险库"); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}
	if err := insertReference(ctx, db, testReferenceID, testProviderID, "token", "ok", "{}"); err != nil {
		t.Fatalf("写入凭据引用失败：%v", err)
	}
}

func columnNames(t *testing.T, db *DB, table string) []string {
	t.Helper()

	rows, err := db.Reader().QueryContext(t.Context(),
		`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("读取 %s 的表结构失败：%v", table, err)
	}
	defer closeRows(t, rows)

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("解析表结构失败：%v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败：%v", err)
	}
	return names
}

func TestCredentialReferences_HasNoColumnThatCouldHoldPlaintext(t *testing.T) {
	// REQ-CRED-001 AC1 的结构审查：列清单必须与预期逐字相符。
	// 用全等而不是「包含」，新增一列就会让这条用例失败 —— 那正是需要有人过目的时刻。
	db := migratedDB(t)

	want := []string{
		"id", "provider_id", "provider_item_ref", "field",
		"service", "account_label", "metadata", "capabilities",
		"health_status", "last_verified_at", "created_at", "updated_at",
	}
	got := columnNames(t, db, "credential_references")

	if len(got) != len(want) {
		t.Fatalf("credential_references 的列是 %v，期望 %v", got, want)
	}
	for index, name := range want {
		if got[index] != name {
			t.Errorf("第 %d 列是 %q，期望 %q", index, got[index], name)
		}
	}
}

func TestCredentialTables_ColumnNames_DoNotSuggestStoredSecrets(t *testing.T) {
	// 上一条用例盯住 credential_references 一张表；这条覆盖凭据链上的三张表，
	// 对将来新增的列同样生效。
	db := migratedDB(t)

	for _, table := range []string{"credential_providers", "credential_references", "identities"} {
		for _, column := range columnNames(t, db, table) {
			normalized := strings.ReplaceAll(strings.ToLower(column), "-", "_")
			for _, word := range secretColumnWords {
				if strings.Contains(normalized, word) {
					t.Errorf("%s.%s 的名字命中 %q，凭据明文不得入库", table, column, word)
				}
			}
		}
	}
}

func TestCredentialProviders_KindIsLimitedToImplementedProviders(t *testing.T) {
	// REQ-CRED-006 AC1：本期只实现三种。存进一个取不出凭据的来源没有意义。
	ctx := t.Context()
	db := migratedDB(t)

	for _, kind := range []string{"1password", "macos-keychain", "local-vault"} {
		if err := insertProvider(ctx, db, "01K1P"+kind, kind, "标签"+kind); err != nil {
			t.Errorf("来源种类 %q 被拒绝：%v", kind, err)
		}
	}

	for _, kind := range []string{"bitwarden", "hashicorp-vault", "env-import"} {
		if err := insertProvider(ctx, db, "01K1X"+kind, kind, "标签"+kind); err == nil {
			t.Errorf("本期不实现的来源种类 %q 被接受了", kind)
		}
	}
}

func TestCredentialProviders_DuplicateKindAndLabel_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertProvider(ctx, db, testProviderID, "1password", "个人保险库"); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertProvider(ctx, db, "01K1PROVIDER000000000000002", "1password", "个人保险库"); err == nil {
		t.Error("同种类下的同名来源被插入了第二行")
	}
	// 换一个种类就应当允许：唯一性是 (kind, label) 而不是 label 单列。
	if err := insertProvider(ctx, db, "01K1PROVIDER000000000000003", "local-vault", "个人保险库"); err != nil {
		t.Errorf("不同种类的同名来源被拒绝：%v", err)
	}

	assertIndexIsUnique(t, db, "credential_providers", "uq_credential_providers_kind_label")
}

func TestCredentialReferences_HealthStatus_IsLimitedToItsSet(t *testing.T) {
	// REQ-CRED-005：三个取值各有后续行为，第四个取值会让那些判断落空。
	ctx := t.Context()
	db := migratedDB(t)
	if err := insertProvider(ctx, db, testProviderID, "1password", "个人保险库"); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}

	for _, status := range []string{"ok", "needs_reauth", "unavailable"} {
		if err := insertReference(ctx, db, "01K1R"+status, testProviderID, status, status, "{}"); err != nil {
			t.Errorf("健康状态 %q 被拒绝：%v", status, err)
		}
	}
	if err := insertReference(ctx, db, "01K1RBAD", testProviderID, "token", "degraded", "{}"); err == nil {
		t.Error("健康状态 \"degraded\" 被接受了")
	}
}

func TestCredentialReferences_NonJSONMetadata_IsRejected(t *testing.T) {
	// metadata 与 capabilities 是结构化数据，存进去的必须是能读回来的 JSON。
	ctx := t.Context()
	db := migratedDB(t)
	if err := insertProvider(ctx, db, testProviderID, "1password", "个人保险库"); err != nil {
		t.Fatalf("写入凭据来源失败：%v", err)
	}

	if err := insertReference(ctx, db, testReferenceID, testProviderID, "token", "ok", "not json"); err == nil {
		t.Error("非 JSON 的 metadata 被接受了")
	}
}

func TestCredentialReferences_DuplicateProviderItemField_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)
	seedCredentialChain(t, db)

	if err := insertReference(ctx, db, "01K1REFERENCE00000000000002", testProviderID, "token", "ok", "{}"); err == nil {
		t.Error("同一来源下的同一字段被登记了第二次")
	}
	// 换一个字段就应当允许。
	if err := insertReference(ctx, db, "01K1REFERENCE00000000000003", testProviderID, "password", "ok", "{}"); err != nil {
		t.Errorf("同一条目的另一个字段被拒绝：%v", err)
	}

	assertIndexIsUnique(t, db, "credential_references", "uq_credential_references_provider_item_field")
}

func TestCredentialReferences_UnknownProvider_IsRejected(t *testing.T) {
	ctx := t.Context()
	db := migratedDB(t)

	if err := insertReference(ctx, db, testReferenceID, "01K1MISSING000000000000000", "token", "ok", "{}"); err == nil {
		t.Error("指向不存在来源的引用被插入了")
	}
}

func TestIdentities_EnumColumns_AreLimitedToTheirSets(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name        string
		environment string
		isDefault   int
		accepted    bool
	}{
		{name: "生产环境", environment: "production", isDefault: 1, accepted: true},
		{name: "非生产环境", environment: "non-production", isDefault: 0, accepted: true},
		{name: "未知环境", environment: "staging", isDefault: 0, accepted: false},
		{name: "is_default 不是 0 或 1", environment: "production", isDefault: 2, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := migratedDB(t)
			seedCredentialChain(t, db)

			err := insertIdentity(ctx, db, testIdentityID, "github", "work", testCase.environment, testCase.isDefault)
			if testCase.accepted && err != nil {
				t.Errorf("合法取值被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("非法取值被接受了")
			}
		})
	}
}

func TestIdentities_DuplicateServiceAndAccountLabel_IsRejected(t *testing.T) {
	// 数据库规则点名的唯一索引。
	ctx := t.Context()
	db := migratedDB(t)
	seedCredentialChain(t, db)

	if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
		t.Fatalf("首次插入失败：%v", err)
	}
	if err := insertIdentity(ctx, db, "01K1IDENTITY000000000000002", "github", "work", "production", 0); err == nil {
		t.Error("同一服务下的同名账户被插入了第二行")
	}
	// REQ-IDENT-001 AC1：同一 service 下可以有多个身份，只要账户名不同。
	if err := insertIdentity(ctx, db, "01K1IDENTITY000000000000003", "github", "personal", "non-production", 0); err != nil {
		t.Errorf("同一服务下的另一个账户被拒绝：%v", err)
	}

	assertIndexIsUnique(t, db, "identities", "uq_identities_service_label")
}

func TestIdentities_CredentialReference_IsRequiredAndRestrictsDeletion(t *testing.T) {
	// 取不到凭据的身份匹配上了也执行不了，所以外键非空；
	// 而被身份引用着的凭据不能被删掉，否则审计追溯断链（§4.2 RESTRICT）。
	ctx := t.Context()
	db := migratedDB(t)
	seedCredentialChain(t, db)

	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO identities (id, service, account_label, environment, is_default, status,
			credential_reference_id, created_at, updated_at)
		 VALUES (?, 'github', 'work', 'production', 1, 'ok', NULL, ?, ?)`,
		testIdentityID, testInstant, testInstant); err == nil {
		t.Error("没有凭据引用的身份被插入了")
	}

	if err := insertIdentity(ctx, db, testIdentityID, "github", "work", "production", 1); err != nil {
		t.Fatalf("写入身份失败：%v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM credential_references WHERE id = ?`, testReferenceID); err == nil {
		t.Error("仍被身份引用的凭据引用被删除了")
	}
	if _, err := db.Writer().ExecContext(ctx,
		`DELETE FROM credential_providers WHERE id = ?`, testProviderID); err == nil {
		t.Error("仍被引用的凭据来源被删除了")
	}
}
