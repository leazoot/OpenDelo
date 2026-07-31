package decision

import "strings"

/*
 * 禁止列表（REQ-DECIDE-004、PRD §3.2）。
 *
 * 五类操作永久拒绝，**不提供审批入口** —— 它们不是「风险高到要人确认」，
 * 而是「无论谁确认都不该发生」。放在第一分支求值：一个想拿走凭据的请求，
 * 不该因为风险算得低就走到自动放行那一格。
 */

// Forbidden 是禁止列表的类别。
type Forbidden string

const (
	// ForbiddenCredentialRequest 是 Agent 请求凭据本身。
	//
	//nolint:gosec // G101 按标识符名里的 credential 一词误报：这是禁止类别的枚举标签，
	// 会出现在账本与 API 响应里，不是任何形式的凭据。
	ForbiddenCredentialRequest Forbidden = "credential_request"
	// ForbiddenVaultExport 是 Agent 请求读取完整保险库。
	ForbiddenVaultExport Forbidden = "vault_export"
	// ForbiddenPrivilegeEscalation 是 Agent 请求扩大自身权限。
	ForbiddenPrivilegeEscalation Forbidden = "privilege_escalation"
	// ForbiddenAuditDisable 是 Agent 请求关闭审计。
	ForbiddenAuditDisable Forbidden = "audit_disable"
	// ForbiddenSelfConfiguration 是 Agent 请求修改 OpenDelo 自身配置。
	ForbiddenSelfConfiguration Forbidden = "self_configuration"
)

// SelfService 是 OpenDelo 自身在能力表中的保留服务名。
//
// 指向它的请求一律落在禁止列表里：Agent 不得配置、削弱或扩大它自己所处的那条边界。
// 落不进前四类的按 self_configuration 处理 —— 默认是拒绝，不是放行。
const SelfService = "opendelo"

// 凭据类名词与读取类动词。两者同时出现才算「请求凭据本身」。
//
// 只看名词会把「创建 API Token」也拦下 —— 那是 PRD §12.3 的高风险操作，
// 要人确认，但不是禁止的。区别在动词：读出来的是现成的凭据，造出来的不是。
var (
	credentialNouns = []string{
		"credential", "secret", "token", "apikey", "password", "privatekey", "passphrase",
	}
	vaultNouns = []string{"vault", "keychain", "keyring"}
	readVerbs  = []string{
		"read", "get", "export", "reveal", "show", "dump", "fetch", "download", "list", "unlock",
	}
)

// 自身服务下的操作按关键词分到前四类，落不进去的归 self_configuration。
var (
	auditWords     = []string{"audit", "ledger"}
	privilegeWords = []string{
		"scope", "permission", "privilege", "grant", "role", "policy",
		"lease", "trust", "approval", "automation",
	}
)

// classifyForbidden 判定一次请求是否落在禁止列表里。
//
// 前两类不看服务：向 GitHub 要「那个 token」与向 OpenDelo 要它是同一件事。
// 后三类以服务为准：扩权、关审计、改配置针对的都是 OpenDelo 自己。
func classifyForbidden(service, operation string) (Forbidden, bool) {
	normalized := normalizeOperation(operation)
	reading := containsAny(normalized, readVerbs)

	switch {
	case reading && containsAny(normalized, vaultNouns):
		return ForbiddenVaultExport, true
	case reading && containsAny(normalized, credentialNouns):
		return ForbiddenCredentialRequest, true
	case !strings.EqualFold(service, SelfService):
		return "", false
	case containsAny(normalized, auditWords):
		return ForbiddenAuditDisable, true
	case containsAny(normalized, privilegeWords):
		return ForbiddenPrivilegeEscalation, true
	default:
		return ForbiddenSelfConfiguration, true
	}
}

func containsAny(normalized string, words []string) bool {
	for _, word := range words {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

// normalizeOperation 去掉大小写与分隔符的差别，与 scope 的越权字段识别同一套路：
// 只比全等的话，`vault.export_all` 与 `Credential-Read` 都会漏网。
func normalizeOperation(operation string) string {
	replacer := strings.NewReplacer("-", "", "_", "", ".", "", "/", "", " ", "")
	return replacer.Replace(strings.ToLower(operation))
}
