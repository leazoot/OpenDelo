package decision

import (
	"strings"
	"unicode"
)

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

// Classify 判定一个服务上的一个操作是否落在禁止列表里。
//
// 导出是给**声明期**用的（`transport/mcpsrv` 在把能力翻成工具时问它）：
// 一条索取凭据的能力根本不该出现在工具清单里，而这一判断必须与决策链路
// 用的是同一份实现 —— 两份关键词表会各自漂移，其中一份漏掉的名字就成了
// 一条只在某个面上存在的路（R-46、`.claude/rules/backend.md` §3）。
//
// 判定不做 I/O，也不看请求的其余部分：它只回答「这个名字是不是那五类之一」。
func Classify(service, operation string) (Forbidden, bool) {
	return classifyForbidden(service, operation)
}

// classifyForbidden 判定一次请求是否落在禁止列表里。
//
// 前两类不看服务：向 GitHub 要「那个 token」与向 OpenDelo 要它是同一件事。
// 后三类以服务为准：扩权、关审计、改配置针对的都是 OpenDelo 自己。
func classifyForbidden(service, operation string) (Forbidden, bool) {
	words := segmentsOf(operation)
	reading := mentionsAny(words, readVerbs)

	switch {
	case reading && mentionsAny(words, vaultNouns):
		return ForbiddenVaultExport, true
	case reading && mentionsAny(words, credentialNouns):
		return ForbiddenCredentialRequest, true
	case !strings.EqualFold(service, SelfService):
		return "", false
	case mentionsAny(words, auditWords):
		return ForbiddenAuditDisable, true
	case mentionsAny(words, privilegeWords):
		return ForbiddenPrivilegeEscalation, true
	default:
		return ForbiddenSelfConfiguration, true
	}
}

// mentionsAny 判断关键词是否作为**完整的一段或连续几段**出现。
//
// 不能用子串：`manage_token` 拼起来是 `managetoken`，接缝处凭空长出一个
// `get`，于是 Cloudflare 的 Token 轮换被判成「Agent 在索取凭据」而永久拒绝 ——
// 那是 PRD §12.3 的高风险操作，要人点头，不是不许做。
//
// 允许连续几段拼起来比，是因为词表里有 `apikey`、`privatekey` 这样的复合词，
// 而它们在操作名里写成 `api_key` / `private_key`。
func mentionsAny(words, keywords []string) bool {
	for start := range words {
		joined := ""
		for _, word := range words[start:] {
			joined += word
			for _, keyword := range keywords {
				if joined == keyword {
					return true
				}
			}
			// 复合词最多由三段拼成（api_key、private_key、audit_events）。
			// 不设上限的话，段数一多就退化成「把整个名字拼起来比」。
			if len(joined) > longestKeyword {
				break
			}
		}
	}
	return false
}

// longestKeyword 是词表里最长的那个词的长度，用来给拼接设一个上限。
const longestKeyword = len("privatekey")

// segmentsOf 把操作名切成小写的段。
//
// 分隔符与驼峰都算边界：`vault.export_all`、`Credential-Read`、`readSecret`
// 得到的是同一种形状。段尾的复数 s 去掉 —— `list_secrets` 与 `secret.get`
// 说的是同一件事。
func segmentsOf(operation string) []string {
	words := make([]string, 0, 4)
	current := make([]rune, 0, len(operation))

	flush := func() {
		if len(current) == 0 {
			return
		}
		word := strings.ToLower(string(current))
		// 只对长过 3 个字母的段去复数：`is`、`as` 这类短段去掉 s 之后
		// 会变成另一个词。
		if len(word) > 3 && strings.HasSuffix(word, "s") {
			word = strings.TrimSuffix(word, "s")
		}
		words = append(words, word)
		current = current[:0]
	}

	for _, letter := range operation {
		switch {
		case letter == '-' || letter == '_' || letter == '.' || letter == '/' || letter == ' ':
			flush()
		case unicode.IsUpper(letter):
			flush()
			current = append(current, letter)
		default:
			current = append(current, letter)
		}
	}
	flush()
	return words
}
