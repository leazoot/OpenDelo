package github

import "github.com/Runcoor/opendelo/internal/adapter/registry"

/*
 * GitHub 的能力声明（REQ-ADAPTER-002、PRD §18.1）。
 *
 * 十四项写成一张字面上的表：九个可执行操作覆盖 PRD §18.1 的八个能力域
 * （「读取 Actions」一个域下有两个操作：读运行状态与读日志），
 * 五项高风险操作**只声明不执行**
 * 高风险操作照样出现在表里，
 * 因为「未声明的操作无法被调用」意味着不声明就等于这些操作在决策链路上不存在，
 * 而它们恰恰是最需要被识别、被标成 high、被人工确认的那几个。
 *
 * 每一项的风险标签都由 registry 在注册时与操作性质对校：删除类、权限变更、
 * 读取 Secret 三种性质任一为真而标签不是 high，启动就会失败。
 */

// Service 是本 Adapter 负责的服务名，与 Scope 的 service 维度对应。
const Service = "github"

// 八个 MVP 能力域下的九个可执行操作（REQ-ADAPTER-002）。
//
// 「读取 Actions」拆成两个：只读日志的话，Agent 问不出「CI 过了没有」——
// 而那是它最常问的那件事；只读运行状态的话，日志脱敏（AC3）就没有落点。
const (
	OpReadRepository   = "read_repository"
	OpReadIssue        = "read_issue"
	OpReadPullRequest  = "read_pull_request"
	OpReadActionsRun   = "read_actions_run"
	OpReadActionsLogs  = "read_actions_logs"
	OpCreateIssue      = "create_issue"
	OpCreatePullReques = "create_pull_request"
	OpCreateComment    = "create_comment"
	OpCreateBranch     = "create_branch"
)

// 五项高风险操作：本期只声明风险，不实现执行。
const (
	OpMergeDefaultBranch      = "merge_default_branch"
	OpDeleteRepository        = "delete_repository"
	OpUpdateCollaborator      = "update_collaborator"
	OpUpdateSecret            = "update_secret"
	OpUpdateActionsPermission = "update_actions_permissions"
)

const (
	schemaNoInput = `{"type":"object","additionalProperties":false}`

	schemaCreateIssue = `{"type":"object","required":["title"],` +
		`"properties":{"title":{"type":"string"},"body":{"type":"string"}},` +
		`"additionalProperties":false}`

	schemaCreatePullRequest = `{"type":"object","required":["title","head","base"],` +
		`"properties":{"title":{"type":"string"},"head":{"type":"string"},` +
		`"base":{"type":"string"},"body":{"type":"string"}},"additionalProperties":false}`

	schemaCreateComment = `{"type":"object","required":["body"],` +
		`"properties":{"body":{"type":"string"}},"additionalProperties":false}`

	schemaCreateBranch = `{"type":"object","required":["ref","sha"],` +
		`"properties":{"ref":{"type":"string"},"sha":{"type":"string"}},` +
		`"additionalProperties":false}`

	schemaMerge = `{"type":"object","properties":{"commit_title":{"type":"string"}},` +
		`"additionalProperties":false}`
)

// repositoryScope 是几乎所有 GitHub 操作的最小 Scope：没有 owner 与 repo，
// 「对哪个仓库做这件事」就定不下来，请求不该进入决策（REQ-SCOPE-001）。
func repositoryScope(extra ...string) registry.MinimumScope {
	keys := append([]string{"owner", "repo"}, extra...)
	return registry.MinimumScope{ResourceKeys: keys, RequiresAccount: true}
}

// capabilities 是全部十三项声明。
func capabilities() []registry.Capability {
	return []registry.Capability{
		{
			Operation:      OpReadRepository,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/repos/{owner}/{repo}",
			RedactionRules: []string{},
			ResponseFields: []string{
				"id", "name", "full_name", "private", "description",
				"default_branch", "html_url", "visibility", "archived",
			},
			Rollback:    registry.RollbackNone,
			Idempotency: registry.Idempotent,
		},
		{
			Operation:      OpReadIssue,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope("number"),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/repos/{owner}/{repo}/issues/{number}",
			RedactionRules: []string{},
			ResponseFields: []string{
				"id", "number", "title", "body", "state", "html_url", "labels",
			},
			Rollback:    registry.RollbackNone,
			Idempotency: registry.Idempotent,
		},
		{
			Operation:      OpReadPullRequest,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope("number"),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/repos/{owner}/{repo}/pulls/{number}",
			RedactionRules: []string{},
			ResponseFields: []string{
				"id", "number", "title", "body", "state", "merged",
				"html_url", "head", "base",
			},
			Rollback:    registry.RollbackNone,
			Idempotency: registry.Idempotent,
		},
		{
			Operation:      OpReadActionsRun,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope("run_id"),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/repos/{owner}/{repo}/actions/runs/{run_id}",
			RedactionRules: []string{},
			ResponseFields: []string{
				"id", "name", "status", "conclusion", "run_number",
				"head_branch", "head_sha", "html_url", "created_at", "updated_at",
			},
			Rollback:    registry.RollbackNone,
			Idempotency: registry.Idempotent,
		},
		{
			Operation:    OpReadActionsLogs,
			InputSchema:  schemaNoInput,
			MinimumScope: repositoryScope("run_id"),
			RiskLabel:    registry.RiskLabelLow,
			Method:       "GET",
			Path:         "/repos/{owner}/{repo}/actions/runs/{run_id}/logs",
			// GitHub 会把它知道的 Secret 打上掩码，但它只认得自己知道的那些。
			// 这里再过一遍本地规则（REQ-ADAPTER-002 AC3）。
			RedactionRules: []string{"webhook", "signature"},
			ResponseFields: []string{fieldLogs},
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
		{
			Operation:      OpCreateIssue,
			InputSchema:    schemaCreateIssue,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/repos/{owner}/{repo}/issues",
			RedactionRules: []string{},
			ResponseFields: []string{"id", "number", "title", "state", "html_url"},
			// 建出来的 Issue 可以关掉，但要人去做。
			Rollback:    registry.RollbackManual,
			Idempotency: registry.NonIdempotent,
		},
		{
			Operation:      OpCreatePullReques,
			InputSchema:    schemaCreatePullRequest,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/repos/{owner}/{repo}/pulls",
			RedactionRules: []string{},
			ResponseFields: []string{"id", "number", "title", "state", "html_url", "head", "base"},
			Rollback:       registry.RollbackManual,
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpCreateComment,
			InputSchema:    schemaCreateComment,
			MinimumScope:   repositoryScope("number"),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/repos/{owner}/{repo}/issues/{number}/comments",
			RedactionRules: []string{},
			ResponseFields: []string{"id", "body", "html_url"},
			Rollback:       registry.RollbackManual,
			// 评论会发到别人的通知里，收回不了那一份（PRD §10.5 的对外通信因子）。
			Nature:      registry.Nature{ExternalCommunication: true},
			Idempotency: registry.NonIdempotent,
		},
		{
			Operation:      OpCreateBranch,
			InputSchema:    schemaCreateBranch,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/repos/{owner}/{repo}/git/refs",
			RedactionRules: []string{},
			ResponseFields: []string{"ref", "node_id", "url", "object"},
			Rollback:       registry.RollbackManual,
			Idempotency:    registry.NonIdempotent,
		},

		// ——— 五项高风险：只声明，不执行 ———

		{
			Operation:      OpMergeDefaultBranch,
			InputSchema:    schemaMerge,
			MinimumScope:   repositoryScope("number"),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/repos/{owner}/{repo}/pulls/{number}/merge",
			RedactionRules: []string{},
			ResponseFields: []string{"sha", "merged", "message"},
			// 合进主分支之后，历史已经变了。
			Rollback:    registry.RollbackNone,
			Idempotency: registry.NonIdempotent,
		},
		{
			Operation:      OpDeleteRepository,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "DELETE",
			Path:           "/repos/{owner}/{repo}",
			RedactionRules: []string{},
			ResponseFields: []string{"message"},
			Rollback:       registry.RollbackNone,
			Nature:         registry.Nature{Destructive: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpUpdateCollaborator,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope("username"),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/repos/{owner}/{repo}/collaborators/{username}",
			RedactionRules: []string{},
			ResponseFields: []string{"id", "permissions"},
			Rollback:       registry.RollbackManual,
			Nature:         registry.Nature{PermissionChange: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpUpdateSecret,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope("secret_name"),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/repos/{owner}/{repo}/actions/secrets/{secret_name}",
			RedactionRules: []string{},
			ResponseFields: []string{"message"},
			Rollback:       registry.RollbackNone,
			Nature:         registry.Nature{SecretAccess: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpUpdateActionsPermission,
			InputSchema:    schemaNoInput,
			MinimumScope:   repositoryScope(),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/repos/{owner}/{repo}/actions/permissions",
			RedactionRules: []string{},
			ResponseFields: []string{"enabled", "allowed_actions"},
			Rollback:       registry.RollbackManual,
			Nature:         registry.Nature{PermissionChange: true},
			Idempotency:    registry.NonIdempotent,
		},
	}
}

// executable 是本期实现了执行的八项。其余五项调用时返回 not_implemented ——
// 不用占位实现假装完成。
var executable = map[string]bool{
	OpReadRepository:   true,
	OpReadIssue:        true,
	OpReadPullRequest:  true,
	OpReadActionsRun:   true,
	OpReadActionsLogs:  true,
	OpCreateIssue:      true,
	OpCreatePullReques: true,
	OpCreateComment:    true,
	OpCreateBranch:     true,
}
