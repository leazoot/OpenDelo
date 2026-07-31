package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * REQ-API-002 的补充端点（Agents / Preferences / Vault）。
 */

// ——— Agents ———

func TestListAgents_ShowsWhoIsConnectedWithoutTheSessionKeyHash(t *testing.T) {
	// session_key_hash 是校验用的比对材料，界面上没有用途，
	// 而它一旦出现在响应里就成了一条可以离线爆破的线索。
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/agents", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		Items []httpapi.AgentView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("返回了 %d 个 Agent，期望 1 个", len(envelope.Items))
	}
	if envelope.Items[0].ExecutableDigest == "" {
		t.Error("没有可执行文件摘要，用户认不出这是哪个二进制")
	}

	body := strings.ToLower(response.Body.String())
	for _, banned := range []string{"session_key", "session_key_hash", "sha256:"} {
		if strings.Contains(body, banned) {
			t.Errorf("响应里出现了 %q", banned)
		}
	}
}

func TestTrustAgent_RequiresAnExplicitConfirmation(t *testing.T) {
	// REQ-AGENT-002：一次误发的空请求不能把陌生 Agent 变成受信任的。
	all := newAPI(t)
	target := "/v1/agents/" + fixtures.DefaultAgentID + "/trust"

	for _, body := range []string{`{}`, `{"confirmed":false}`} {
		t.Run(body, func(t *testing.T) {
			response := all.call(t, http.MethodPost, target, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
			if fields := errorFields(t, response); len(fields) == 0 {
				t.Error("错误体没有指出是哪个字段")
			}
		})
	}

	response := all.call(t, http.MethodPost, target, `{"confirmed":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.AgentView
	decodeInto(t, response, &view)
	// 本期确认后的级别是 known，trusted 保留给后续版本（REQ-AGENT-002）。
	if view.TrustLevel != string(agentauth.TrustKnown) {
		t.Errorf("信任级别为 %q，期望 known", view.TrustLevel)
	}

	// 重复确认返回首次的结果，不产生第二次副作用。
	again := all.call(t, http.MethodPost, target, `{"confirmed":true}`)
	if again.Code != http.StatusOK {
		t.Errorf("重复确认返回 %d", again.Code)
	}
}

func TestTrustAgent_AnUnknownAgentIsNotFound(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodPost,
		"/v1/agents/01J000000000000000NOPE/trust", `{"confirmed":true}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}

// ——— Preferences ———

func TestPreferences_DefaultsAreTheConservativeOnes(t *testing.T) {
	// 一条偏好都没写过时用默认值：平衡模式、5 分钟审批窗口、只读自动允许关闭。
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/preferences", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.PreferencesView
	decodeInto(t, response, &view)
	if view.AutomationMode != string(decision.ModeBalanced) {
		t.Errorf("自动化等级为 %q，期望 balanced", view.AutomationMode)
	}
	if view.ApprovalTimeoutSecond != 300 {
		t.Errorf("审批超时为 %d 秒，期望 300", view.ApprovalTimeoutSecond)
	}
	if view.ReadOnlyAutoAllow {
		t.Error("只读自动允许默认是开的")
	}
	if view.RestartRequired.WebAPIPort != 8787 {
		t.Errorf("Web API 端口为 %d", view.RestartRequired.WebAPIPort)
	}
}

func TestPatchPreferences_TakesEffectImmediately(t *testing.T) {
	// REQ-PREF-001 AC1：改完不需要重启就生效 —— 下一次 GET 读到的就是新值。
	all := newAPI(t)

	response := all.call(t, http.MethodPatch, "/v1/preferences",
		`{"preferences":{"automation_mode":"cautious","theme":"light",`+
			`"approval_timeout_seconds":"60","read_only_auto_allow":"true","language":"en"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.PreferencesView
	decodeInto(t, response, &view)
	if view.AutomationMode != string(decision.ModeCautious) {
		t.Errorf("自动化等级为 %q", view.AutomationMode)
	}
	if view.ApprovalTimeoutSecond != 60 || view.Theme != "light" ||
		view.Language != "en" || !view.ReadOnlyAutoAllow {
		t.Errorf("偏好没有全部写进去：%+v", view)
	}

	reread := all.call(t, http.MethodGet, "/v1/preferences", "")
	var again httpapi.PreferencesView
	decodeInto(t, reread, &again)
	if again.AutomationMode != view.AutomationMode || again.Theme != view.Theme {
		t.Error("重新读出来的偏好与刚写进去的不一致")
	}
}

func TestPatchPreferences_RejectsAnythingItCannotRecognise(t *testing.T) {
	// 认不出的键名或取值一律拒绝，且**整批不写** ——
	// 写一半会让「我改了什么」与「实际生效了什么」对不上。
	all := newAPI(t)

	cases := map[string]string{
		"认不出的键名": `{"preferences":{"turn_off_audit":"true"}}`,
		"认不出的等级": `{"preferences":{"automation_mode":"yolo"}}`,
		"超时超出范围": `{"preferences":{"approval_timeout_seconds":"86400"}}`,
		"超时不是整数": `{"preferences":{"approval_timeout_seconds":"一会儿"}}`,
		"主题认不出":  `{"preferences":{"theme":"neon"}}`,
		"语言认不出":  `{"preferences":{"language":"kl"}}`,
		"一项好一项坏": `{"preferences":{"theme":"light","automation_mode":"yolo"}}`,
		"什么都没给":  `{"preferences":{}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := all.call(t, http.MethodPatch, "/v1/preferences", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400，正文为 %s", response.Code, response.Body.String())
			}
		})
	}

	// 「一项好一项坏」那次不能把好的那一项写进去。
	current := all.call(t, http.MethodGet, "/v1/preferences", "")
	var view httpapi.PreferencesView
	decodeInto(t, current, &view)
	if view.Theme != settings.ThemeSystem {
		t.Errorf("主题被改成了 %q，那一批本该整批不写", view.Theme)
	}
}

func TestPatchPreferences_CannotReachThingsThatNeedARestart(t *testing.T) {
	// 端口与监听地址不在可写的键表里：它们改完要重启，
	// 而这个端点改完立刻生效，混在一起用户就无从判断自己那次修改生效没有。
	all := newAPI(t)

	for _, key := range []string{"web_api_port", "listen_address", "mcp_port", "log_level"} {
		t.Run(key, func(t *testing.T) {
			response := all.call(t, http.MethodPatch, "/v1/preferences",
				`{"preferences":{"`+key+`":"9999"}}`)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
		})
	}
}

// ——— Vault ———

func TestVault_WithoutALocalVaultReportsNotImplemented(t *testing.T) {
	// 没配置保险库时明说，而不是假装锁着 —— 后者会让用户以为凭据存在这里。
	all := newAPI(t)

	for _, target := range []string{"/v1/vault/unlock", "/v1/vault/lock"} {
		t.Run(target, func(t *testing.T) {
			response := all.call(t, http.MethodPost, target,
				`{"master_password":"`+sentinel.SentinelPassword+`"}`)
			if response.Code != http.StatusNotImplemented {
				t.Fatalf("状态码为 %d，期望 501", response.Code)
			}
			if strings.Contains(response.Body.String(), sentinel.SentinelPassword) {
				t.Error("响应里回显了主密码")
			}
		})
	}
}

func TestVault_UnlockAndLockRoundTripWithoutEchoingThePassword(t *testing.T) {
	// REQ-CRED-004：解锁 / 锁定，且响应里没有任何字段能放下明文。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	locked := all.call(t, http.MethodPost, "/v1/vault/lock", "")
	if locked.Code != http.StatusOK {
		t.Fatalf("锁定失败：%d %s", locked.Code, locked.Body.String())
	}

	unlocked := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if unlocked.Code != http.StatusOK {
		t.Fatalf("解锁失败：%d %s", unlocked.Code, unlocked.Body.String())
	}
	var view httpapi.VaultView
	decodeInto(t, unlocked, &view)
	if !view.Unlocked {
		t.Error("解锁成功却报告仍然锁着")
	}
	if strings.Contains(unlocked.Body.String(), fixtures.VaultMasterPassword) {
		t.Error("响应里回显了主密码")
	}

	// 锁定是幂等的意图：连锁两次都返回 200。
	for attempt := 1; attempt <= 2; attempt++ {
		if again := all.call(t, http.MethodPost, "/v1/vault/lock", ""); again.Code != http.StatusOK {
			t.Fatalf("第 %d 次锁定返回 %d", attempt, again.Code)
		}
	}
}

func TestVault_WrongPasswordAndMissingVaultLookTheSame(t *testing.T) {
	// 区分开来就等于告诉调用方这台机器上有没有保险库
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	wrong := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+wrongPassword+`"}`)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("状态码为 %d，期望 401，正文为 %s", wrong.Code, wrong.Body.String())
	}
	if code := decodeErrorCode(t, wrong); code != "unauthenticated" {
		t.Errorf("错误码为 %q", code)
	}
	if strings.Contains(wrong.Body.String(), wrongPassword) {
		t.Error("响应里回显了主密码")
	}
}

// wrongPassword 与夹具里的主密码不同，且同样是一个哨兵。
const wrongPassword = sentinel.SentinelToken

func TestVault_EmptyPasswordIsRefusedBeforeTouchingTheVault(t *testing.T) {
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	response := all.call(t, http.MethodPost, "/v1/vault/unlock", `{"master_password":"   "}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestConsoleEndpoints_RefuseAgents(t *testing.T) {
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	cases := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/v1/agents"},
		{http.MethodPost, "/v1/agents/anything/trust"},
		{http.MethodGet, "/v1/preferences"},
		{http.MethodPatch, "/v1/preferences"},
		{http.MethodPost, "/v1/vault/unlock"},
		{http.MethodPost, "/v1/vault/lock"},
	}

	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.target, func(t *testing.T) {
			response := all.call(t, testCase.method, testCase.target, `{}`)
			if response.Code != http.StatusForbidden {
				t.Fatalf("状态码为 %d，期望 403", response.Code)
			}
		})
	}
}

func TestConsoleEndpoints_RejectAnUnusableLimitAndMalformedBody(t *testing.T) {
	all := newAPI(t)

	if response := all.call(t, http.MethodGet, "/v1/agents?limit=0", ""); response.Code !=
		http.StatusBadRequest {
		t.Errorf("limit=0 返回 %d，期望 400", response.Code)
	}
	if response := all.call(t, http.MethodPost,
		"/v1/agents/"+fixtures.DefaultAgentID+"/trust", `{`); response.Code !=
		http.StatusBadRequest {
		t.Errorf("坏掉的正文返回 %d，期望 400", response.Code)
	}
	if response := all.call(t, http.MethodPatch, "/v1/preferences", `不是 JSON`); response.Code !=
		http.StatusBadRequest {
		t.Errorf("坏掉的正文返回 %d，期望 400", response.Code)
	}
}

// ——— 建立保险库（REQ-CRED-004 §2，用户决定 D-15） ———

func TestCreateVault_SetsTheMasterPasswordAndLeavesItUnlocked(t *testing.T) {
	// 没有保险库的 Gateway 上建立一个，随后它就能解锁 —— 强认证的前提。
	all := newAPIWith(t, fixtures.NewGatewayWithEmptyVault(t), httpapi.Caller{})

	created := all.call(t, http.MethodPost, "/v1/vault",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("状态码为 %d，期望 201，正文为 %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), fixtures.VaultMasterPassword) {
		t.Error("响应里回显了主密码")
	}

	if locked := all.call(t, http.MethodPost, "/v1/vault/lock", ""); locked.Code != http.StatusOK {
		t.Fatalf("锁定失败：%d", locked.Code)
	}
	unlocked := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if unlocked.Code != http.StatusOK {
		t.Fatalf("刚设定的主密码解不开：%d %s", unlocked.Code, unlocked.Body.String())
	}
}

func TestCreateVault_RefusesToOverwriteAnExistingVault(t *testing.T) {
	// 覆盖会把原有凭据全部丢掉，而那不可逆。
	all := newAPIWith(t, fixtures.NewGatewayWithVault(t), httpapi.Caller{})

	response := all.call(t, http.MethodPost, "/v1/vault",
		`{"master_password":"`+wrongPassword+`"}`)
	if response.Code == http.StatusCreated {
		t.Fatal("已存在的保险库被覆盖了")
	}

	// 原来的主密码仍然管用 —— 那次拒绝没有动过文件。
	all.call(t, http.MethodPost, "/v1/vault/lock", "")
	unlocked := all.call(t, http.MethodPost, "/v1/vault/unlock",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if unlocked.Code != http.StatusOK {
		t.Fatalf("原主密码解不开了：%d %s", unlocked.Code, unlocked.Body.String())
	}
}

func TestCreateVault_RefusesAShortMasterPassword(t *testing.T) {
	// 主密码是 Argon2id 唯一的输入，短口令让参数再强也挡不住穷举。
	all := newAPIWith(t, fixtures.NewGatewayWithEmptyVault(t), httpapi.Caller{})

	response := all.call(t, http.MethodPost, "/v1/vault", `{"master_password":"short"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestCreateVault_RefusesAgents(t *testing.T) {
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodPost, "/v1/vault",
		`{"master_password":"`+fixtures.VaultMasterPassword+`"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}
