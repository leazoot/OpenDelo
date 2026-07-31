package httpapi

import (
	"net/http"
	"strings"

	"github.com/Runcoor/opendelo/internal/credential/localvault"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * REQ-API-002 的补充端点：Agents、Preferences、Vault。
 *
 * 三组都是**人的配置面**，Agent 一律 403（REQ-DECIDE-004）。
 * 解锁端点是唯一一处接收明文的地方，而它把明文原样交给 credential 那一层
 * 就地装箱清零 —— secret.Value 在本包不可见（ADR-002，由 test/arch 强制）。
 */

// trustBody 是 POST /v1/agents/:id/trust 的请求体。
type trustBody struct {
	// Confirmed 必须显式为真。省略它就把「信任这个 Agent」变成了默认动作。
	Confirmed bool `json:"confirmed"`
}

// unlockBody 是 POST /v1/vault/unlock 的请求体。
type unlockBody struct {
	// MasterPassword 只在这一次请求里存在。响应里没有任何字段能回显它。
	MasterPassword string `json:"master_password"`
}

// patchPreferencesBody 是 PATCH /v1/preferences 的请求体。
//
// 键值形式而不是一个宽结构体：偏好是一张编译期定死的键表
// （见 platform/settings），加一项不需要改这里的形状，
// 而认不出的键名一律被拒 —— 悄悄忽略会让调用方以为自己改了什么。
type patchPreferencesBody struct {
	Preferences map[string]string `json:"preferences"`
}

// listAgents 返回全部 Agent（Identities 页面的 Agents 列）。
func (e *endpoints) listAgents(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "Agent 管理"); err != nil {
		return
	}

	limit, err := limitFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	agents, err := e.services.Agents.Agents(r.Context(), limit)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	items := make([]AgentView, 0, len(agents))
	for _, agent := range agents {
		items = append(items, agentView(agent))
	}
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[AgentView]{Items: items})
}

// trustAgent 把一个 Agent 标记为已确认（REQ-AGENT-002）。
//
// 必须显式确认：请求体里 confirmed 不为真就拒绝，这样一次误发的空请求
// 不会把一个陌生 Agent 变成受信任的。
func (e *endpoints) trustAgent(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "Agent 管理"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	var body trustBody
	if err = decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if !body.Confirmed {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("信任一个 Agent 必须显式确认"), "confirmed")
		return
	}

	trusted, err := e.services.AgentAuth.ConfirmTrust(r.Context(), id)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK, agentView(trusted))
}

// preferences 把 GET 与 PATCH 分派到各自的处理器。
func (e *endpoints) preferences(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		e.patchPreferences(w, r)
		return
	}
	e.showPreferences(w, r)
}

// showPreferences 返回当前偏好。
func (e *endpoints) showPreferences(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "偏好"); err != nil {
		return
	}

	current, warnings, err := e.services.Preferences.Load(r.Context())
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK,
		preferencesView(current, e.services.Config, warnings))
}

// patchPreferences 改一批偏好（REQ-PREF-001）。
//
// 只写运行期偏好。端口与监听地址在响应里是只读的：改它们要重启进程，
// 而这个端点改完立刻生效 —— 把两种生效方式放进同一个动作，
// 用户就无从知道自己刚才那次修改到底生效了没有。
func (e *endpoints) patchPreferences(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "偏好"); err != nil {
		return
	}

	var body patchPreferencesBody
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if len(body.Preferences) == 0 {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有给出要修改的偏好"), "preferences")
		return
	}

	saved, err := e.services.Preferences.Save(r.Context(), body.Preferences)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK,
		preferencesView(saved, e.services.Config, nil))
}

// createVault 建立本地保险库并设定主密码（REQ-CRED-004 §2，用户决定 D-15）。
//
// **已存在时拒绝且不覆盖**：覆盖会把原有凭据全部丢掉，而那不可逆
// （`localvault.Create` 的契约）。没有它，强认证在真实安装上无从谈起 ——
// 保险库文件不存在时任何主密码都解不开。
func (e *endpoints) createVault(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "保险库"); err != nil {
		return
	}
	if e.services.Vault == nil {
		e.fail(w, r, apperr.New(apperr.CodeNotImplemented).
			WithDetail("这台 Gateway 没有配置本地保险库"))
		return
	}

	var body unlockBody
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if len(strings.TrimSpace(body.MasterPassword)) < minMasterPasswordLength {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("主密码至少 12 个字符"), "master_password")
		return
	}

	// 与解锁同理：明文交给 credential 那一层就地装箱并清零（ADR-002）。
	if err := e.services.Vault.CreateWith([]byte(body.MasterPassword)); err != nil {
		e.fail(w, r, err)
		return
	}
	// Create 之后保险库已经是解锁状态，照实说。
	writeJSON(w, r, e.logger, http.StatusCreated, vaultView(true))
}

// minMasterPasswordLength 是主密码的最短长度。
//
// 主密码是 Argon2id 的唯一输入，短口令让参数再强也挡不住穷举。
// 取 12 而不是 8：这个口令不常输，
// 且它护着的是这台机器上全部凭据。
const minMasterPasswordLength = 12

// unlockVault 解锁本地保险库（REQ-CRED-004）。
//
// 失败信息不区分「密码错误」与「保险库不存在」：区分开来就等于告诉调用方
// 这台机器上有没有保险库。
func (e *endpoints) unlockVault(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "保险库"); err != nil {
		return
	}
	if e.services.Vault == nil {
		e.fail(w, r, apperr.New(apperr.CodeNotImplemented).
			WithDetail("这台 Gateway 没有配置本地保险库"))
		return
	}

	var body unlockBody
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if strings.TrimSpace(body.MasterPassword) == "" {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有给出主密码"), "master_password")
		return
	}

	// 明文交给 credential 那一层就地装箱并清零：secret.Value 在本包不可见
	// （ADR-002）。JSON 解出来的那个字符串是不可变的、清不掉的，
	// 这是这条路径已知的残留面 —— 它只活到这次请求被 GC 为止。
	outcome, err := e.services.Vault.UnlockWith([]byte(body.MasterPassword))
	if outcome.LockoutBegan {
		// 账本先于答复：写不进去时这次尝试整个失败（ADR-004）。锁定已经在
		// 保险库里生效了 —— 记不下来时宁可让调用方看到一次内部错误，
		// 也不能给出一条不在账本上的安全事件。
		if auditErr := e.services.Pipeline.RecordStrongAuthLocked(r.Context(),
			logging.OperationIDFrom(r.Context()),
			int(localvault.LockoutDuration.Seconds())); auditErr != nil {
			e.fail(w, r, auditErr)
			return
		}
	}
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK, vaultView(true))
}

// lockVault 锁上本地保险库。
//
// 已经锁着时也返回 200：锁上是一个幂等的意图，而不是一次状态转移。
func (e *endpoints) lockVault(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "保险库"); err != nil {
		return
	}
	if e.services.Vault == nil {
		e.fail(w, r, apperr.New(apperr.CodeNotImplemented).
			WithDetail("这台 Gateway 没有配置本地保险库"))
		return
	}

	e.services.Vault.Lock()
	writeJSON(w, r, e.logger, http.StatusOK, vaultView(false))
}
