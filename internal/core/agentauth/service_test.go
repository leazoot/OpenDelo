package agentauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * core/agentauth 的行为用例（REQ-AGENT-001/002/003）。
 *
 * 仓储用真实 SQLite 而不是 mock：约束、外键与唯一索引本身就是这条链路的一部分，
 * 换成内存假实现测到的就不再是入库后的行为。
 */

type harness struct {
	service *agentauth.Service
	agents  *repo.Agents
	db      *store.DB
	clock   *clock.Fixed
	ctx     context.Context
}

func newHarness(t *testing.T, options ...func(*agentauth.Options)) harness {
	t.Helper()

	db := fixtures.MigratedDB(t)
	moment := clock.NewFixed(fixtures.Instant)
	agents := repo.NewAgents(db)

	serviceOptions := agentauth.Options{
		Agents:     agents,
		Devices:    repo.NewDevices(db),
		Workspaces: repo.NewWorkspaces(db),
		Clock:      moment,
		IDs:        ulid.New(moment),
	}
	for _, apply := range options {
		apply(&serviceOptions)
	}

	service, err := agentauth.NewService(serviceOptions)
	if err != nil {
		t.Fatalf("构造 agentauth 服务失败：%v", err)
	}
	return harness{service: service, agents: agents, db: db, clock: moment, ctx: t.Context()}
}

func mustRegister(t *testing.T, h harness, registration agentauth.Registration) agentauth.Registered {
	t.Helper()

	registered, err := h.service.Register(h.ctx, registration)
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	return registered
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

// ——— 注册：身份绑定的完整性（REQ-AGENT-001 AC3、AC4）———

func TestRegister_OnlyName_IsRejected(t *testing.T) {
	// AC4：仅提供 name=claude-code 而无进程上下文的注册必须被拒。
	h := newHarness(t)

	_, err := h.service.Register(h.ctx, agentauth.Registration{
		Name: "claude-code",
		Type: agentauth.TypeClaudeCode,
	})
	assertCode(t, err, apperr.CodeAgentIdentityUnverifiable)

	if count := countRows(t, h.db, "agents"); count != 0 {
		t.Errorf("被拒的注册留下了 %d 条 Agent 记录", count)
	}
}

func TestRegister_EveryBindingField_IsRequired(t *testing.T) {
	// 九项绑定缺任何一项都无法确认「是我启动的那个进程」，一律拒绝。
	cases := []struct {
		name  string
		blank fixtures.RegistrationOption
	}{
		{"缺可执行文件哈希", func(r *agentauth.Registration) { r.ExecutableHash = "" }},
		{"缺可执行文件路径", func(r *agentauth.Registration) { r.ExecutablePath = "" }},
		{"缺进程号", func(r *agentauth.Registration) { r.PID = 0 }},
		{"父进程号为负", func(r *agentauth.Registration) { r.ParentPID = -1 }},
		{"缺系统用户", func(r *agentauth.Registration) { r.OSUser = "" }},
		{"缺设备指纹", func(r *agentauth.Registration) { r.DeviceFingerprint = "" }},
		{"缺工作区路径", func(r *agentauth.Registration) { r.WorkspacePath = "" }},
		{"缺项目指纹", func(r *agentauth.Registration) { r.ProjectFingerprint = "" }},
		{"缺启动时间", func(r *agentauth.Registration) { r.StartedAt = time.Time{} }},
		{"可执行文件路径不是绝对路径", func(r *agentauth.Registration) { r.ExecutablePath = "bin/claude" }},
		{"工作区路径不是绝对路径", func(r *agentauth.Registration) { r.WorkspacePath = "projects/opendelo" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)

			_, err := h.service.Register(h.ctx, fixtures.Registration(testCase.blank))
			assertCode(t, err, apperr.CodeAgentIdentityUnverifiable)
		})
	}
}

func TestRegister_NoParentProcess_IsAllowed(t *testing.T) {
	// AC3 显式允许 parent_pid 为 0：init 直接拉起的进程没有父进程。
	h := newHarness(t)

	registered := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.ParentPID = 0
	}))
	if registered.Agent.ParentPID != 0 {
		t.Errorf("父进程号为 %d，期望 0", registered.Agent.ParentPID)
	}
}

func TestRegister_PersistsAllNineBindings(t *testing.T) {
	// AC3：注册后九项绑定在库中均非空。
	h := newHarness(t)
	registration := fixtures.Registration()

	registered := mustRegister(t, h, registration)

	stored, err := h.agents.AgentByID(h.ctx, registered.Agent.ID)
	if err != nil {
		t.Fatalf("读取 Agent 失败：%v", err)
	}
	checks := map[string]bool{
		"executable_hash":  stored.ExecutableHash == registration.ExecutableHash,
		"executable_path":  stored.ExecutablePath == registration.ExecutablePath,
		"pid":              stored.PID == registration.PID,
		"parent_pid":       stored.ParentPID == registration.ParentPID,
		"os_user":          stored.OSUser == registration.OSUser,
		"device_id":        stored.DeviceID != "",
		"workspace_id":     stored.WorkspaceID != "",
		"started_at":       stored.StartedAt.Equal(registration.StartedAt),
		"session_key_hash": strings.HasPrefix(stored.SessionKeyHash, agentauth.SessionKeyHashPrefix),
	}
	for column, ok := range checks {
		if !ok {
			t.Errorf("绑定项 %s 未按提交内容落库", column)
		}
	}
	if len(checks) != 9 {
		t.Fatalf("只核对了 %d 项绑定，REQ-AGENT-001 要求 9 项", len(checks))
	}
}

func TestRegister_NewAgent_IsUnverifiedAndActive(t *testing.T) {
	// REQ-AGENT-002 AC1：新注册的信任等级是 unverified。
	h := newHarness(t)

	registered := mustRegister(t, h, fixtures.Registration())

	if registered.Agent.TrustLevel != agentauth.TrustUnverified {
		t.Errorf("信任等级为 %s，期望 unverified", registered.Agent.TrustLevel)
	}
	if registered.Agent.Status != agentauth.StatusActive {
		t.Errorf("状态为 %s，期望 active", registered.Agent.Status)
	}
	if registered.IsRebind {
		t.Error("首次注册被当成了重新绑定")
	}
}

func TestRegister_EveryAgentType_IsAccepted(t *testing.T) {
	// REQ-AGENT-003 AC1：五种类型均可注册。
	types := []agentauth.AgentType{
		agentauth.TypeClaudeCode,
		agentauth.TypeCodex,
		agentauth.TypeGeminiCLI,
		agentauth.TypeOpenCode,
		agentauth.TypeGeneric,
	}

	for _, agentType := range types {
		t.Run(string(agentType), func(t *testing.T) {
			h := newHarness(t)

			registered := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
				r.Type = agentType
			}))
			if registered.Agent.Type != agentType {
				t.Errorf("类型为 %s，期望 %s", registered.Agent.Type, agentType)
			}
		})
	}
}

func TestRegister_UnknownAgentType_IsRejected(t *testing.T) {
	h := newHarness(t)

	_, err := h.service.Register(h.ctx, fixtures.Registration(func(r *agentauth.Registration) {
		r.Type = "totally-new-agent"
	}))
	assertCode(t, err, apperr.CodeInvalidRequest)

	// 校验必须发生在任何副作用之前。只断言错误码不够：把校验删掉后数据库的
	// CHECK 也会给出同一个码，区别在于那时设备与工作区已经被写进去了。
	for _, table := range []string{"devices", "workspaces", "agents"} {
		if count := countRows(t, h.db, table); count != 0 {
			t.Errorf("类型非法的注册在 %s 留下了 %d 行", table, count)
		}
	}
}

func TestRegister_MissingDisplayFields_AreRejectedAsInvalidRequest(t *testing.T) {
	// 名称与设备名不是身份的一部分，缺了是请求不合法，而不是身份无法确认 ——
	// 两者用同一个码会让接入面分不清该补字段还是该重新采集进程上下文。
	cases := []struct {
		name  string
		blank fixtures.RegistrationOption
	}{
		{"缺 Agent 名称", func(r *agentauth.Registration) { r.Name = "" }},
		{"缺设备名称", func(r *agentauth.Registration) { r.DeviceName = "" }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)

			_, err := h.service.Register(h.ctx, fixtures.Registration(testCase.blank))
			assertCode(t, err, apperr.CodeInvalidRequest)
		})
	}
}

func TestRegister_UnseenDevice_IsUntrustedUntilConfirmed(t *testing.T) {
	h := newHarness(t)
	devices := repo.NewDevices(h.db)

	registered := mustRegister(t, h, fixtures.Registration())

	device, err := devices.DeviceByID(h.ctx, registered.Agent.DeviceID)
	if err != nil {
		t.Fatalf("读取设备失败：%v", err)
	}
	if device.TrustStatus != agentauth.DeviceUntrusted {
		t.Errorf("首次见到的设备信任状态为 %s，期望 untrusted", device.TrustStatus)
	}
}

func TestRegister_WorkspacePathIsNormalized(t *testing.T) {
	// 同一个目录必须只对应一条工作区记录，否则授权会被切成两份。
	h := newHarness(t)

	first := mustRegister(t, h, fixtures.Registration())
	second := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.WorkspacePath = "/Users/tester/projects/../projects/opendelo/"
	}))

	if first.Agent.WorkspaceID != second.Agent.WorkspaceID {
		t.Errorf("同一目录产生了两条工作区记录：%s 与 %s", first.Agent.WorkspaceID, second.Agent.WorkspaceID)
	}
}

func TestRegister_ProjectFingerprintChanged_IsReported(t *testing.T) {
	// 项目指纹变化要让依赖它的绑定重新确认（REQ-IDENT-003 AC3）。
	// 本任务只负责把变化如实报出来，失效由 core/trust 处理。
	h := newHarness(t)
	mustRegister(t, h, fixtures.Registration())

	registered := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.ProjectFingerprint = "fp-project-0002"
	}))

	if !registered.ProjectFingerprintChanged {
		t.Error("项目指纹变了却没有被报出来")
	}

	unchanged := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.ProjectFingerprint = "fp-project-0002"
	}))
	if unchanged.ProjectFingerprintChanged {
		t.Error("指纹没变却报了变化")
	}
}

// ——— 重启（REQ-AGENT-001 行为要求 3、AC2）———

func TestRegister_Restart_KeepsTheAgentAndInvalidatesTheOldKey(t *testing.T) {
	h := newHarness(t)
	first := mustRegister(t, h, fixtures.Registration())

	// 重启：进程号与启动时间都变了，可执行文件没变。
	h.clock.Advance(time.Minute)
	second := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.PID = 5678
		r.ParentPID = 5677
		r.StartedAt = fixtures.Instant.Add(time.Minute)
	}))

	if !second.IsRebind {
		t.Error("重启没有命中原有身份，被当成了新 Agent")
	}
	if second.Agent.ID != first.Agent.ID {
		t.Fatalf("重启后 Agent 主键从 %s 变成了 %s，绑定其上的记忆会全部失联", first.Agent.ID, second.Agent.ID)
	}
	if second.Agent.PID != 5678 {
		t.Errorf("重启后进程号为 %d，期望 5678", second.Agent.PID)
	}

	// 行为要求 3：旧 Session Key 立即失效。
	_, err := h.service.Authenticate(h.ctx, first.SessionKey, agentauth.Presented{})
	assertCode(t, err, apperr.CodeUnauthenticated)

	if _, err := h.service.Authenticate(h.ctx, second.SessionKey, agentauth.Presented{}); err != nil {
		t.Fatalf("新 Session Key 无法通过校验：%v", err)
	}
}

func TestRegister_ExecutableChanged_CreatesANewAgentAndLosesTheOldMemories(t *testing.T) {
	// AC2：改一个字节后重启，注册返回新的 agent 记录，旧 Trust Memory 查询为空。
	h := newHarness(t)
	memory := fixtures.SeedMemoryChain(t, h.db)
	memories := repo.NewTrustMemories(h.db)

	// 铺底的 Agent 与 Registration 夹具描述的是同一个进程，注册因此命中它。
	first := mustRegister(t, h, fixtures.Registration())
	if first.Agent.ID != fixtures.DefaultAgentID {
		t.Fatalf("注册没有命中已有身份，得到 %s", first.Agent.ID)
	}

	before, err := memories.MatchMemories(h.ctx, first.Agent.ID, first.Agent.WorkspaceID, memory.Service, 10)
	if err != nil {
		t.Fatalf("匹配记忆失败：%v", err)
	}
	if len(before) != 1 {
		t.Fatalf("改动前匹配到 %d 条记忆，期望 1 条", len(before))
	}

	h.clock.Advance(time.Minute)
	second := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.ExecutableHash = "sha256:6f1d0c9a2c" // 只差最后一个字节
		r.PID = 5678
		r.StartedAt = fixtures.Instant.Add(time.Minute)
	}))

	if second.Agent.ID == first.Agent.ID {
		t.Fatal("可执行文件变了却复用了同一条 Agent 记录")
	}
	if second.IsRebind {
		t.Error("可执行文件变了却被当成同一个身份的重新绑定")
	}

	after, err := memories.MatchMemories(h.ctx, second.Agent.ID, second.Agent.WorkspaceID, memory.Service, 10)
	if err != nil {
		t.Fatalf("匹配记忆失败：%v", err)
	}
	if len(after) != 0 {
		t.Errorf("哈希变化后仍匹配到 %d 条旧记忆", len(after))
	}
}

// ——— 校验（REQ-AGENT-001 AC1、异常状态）———

func TestAuthenticate_WithoutAValidSessionKey_IsRejected(t *testing.T) {
	// AC1：身份完全由 Session Key 决定 —— 本方法根本不接受 agent_id 参数，
	// 所以「伪造 agent_id」在类型上就无从谈起。伪造的密钥一律 401。
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	forged := []struct {
		name string
		key  agentauth.SessionKey
	}{
		{"空密钥", agentauth.SessionKey{}},
		{"随手编的密钥", agentauth.NewSessionKey("not-a-real-session-key")},
		{"把 Agent 主键当密钥用", agentauth.NewSessionKey(registered.Agent.ID)},
		{"把密钥的哈希当密钥用", agentauth.NewSessionKey(registered.Agent.SessionKeyHash)},
	}

	for _, testCase := range forged {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := h.service.Authenticate(h.ctx, testCase.key, agentauth.Presented{})
			assertCode(t, err, apperr.CodeUnauthenticated)
		})
	}

	// 空密钥要在查库之前就被挡下：拿空串去查库，命中与否取决于表里有没有一行
	// 恰好存着空串的哈希，那是一个不该存在的可能性。对外的码相同，诊断信息不同。
	_, emptyErr := h.service.Authenticate(h.ctx, agentauth.SessionKey{}, agentauth.Presented{})
	if !strings.Contains(emptyErr.Error(), "未携带 Session Key") {
		t.Errorf("空密钥的诊断信息是 %q，说明它被当成一个普通的查找键送进了数据库", emptyErr)
	}

	// AC1 的后半句：这条路径不产生任何 Capability Request 记录。
	if count := countRows(t, h.db, "capability_requests"); count != 0 {
		t.Errorf("被拒的校验留下了 %d 条能力请求", count)
	}
}

func TestAuthenticate_ExpiredSession_IsNotRenewed(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())
	expiresAt := registered.Agent.SessionExpiresAt

	h.clock.Advance(agentauth.DefaultSessionTTL)

	_, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{})
	assertCode(t, err, apperr.CodeSessionExpired)

	stored, err := h.agents.AgentByID(h.ctx, registered.Agent.ID)
	if err != nil {
		t.Fatalf("读取 Agent 失败：%v", err)
	}
	if !stored.SessionExpiresAt.Equal(expiresAt) {
		t.Errorf("过期时刻被改成了 %s，Session Key 不得自动续期", stored.SessionExpiresAt)
	}
}

func TestAuthenticate_SamePIDDifferentExecutable_IsIdentityMismatch(t *testing.T) {
	// 异常状态：同一 PID 提交了与已注册记录不同的可执行文件哈希 → 拒绝。
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	_, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{
		PID:            registered.Agent.PID,
		ExecutableHash: "sha256:something-else",
	})
	assertCode(t, err, apperr.CodeAgentIdentityUnverifiable)

	if !errors.Is(err, agentauth.ErrIdentityMismatch) {
		t.Error("接入面无法认出这是身份不符，也就无从记 agent.identity_mismatch")
	}
}

func TestAuthenticate_AnotherProcessPresentingTheKey_IsRejected(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	_, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{
		PID:            registered.Agent.PID + 1,
		ExecutableHash: registered.Agent.ExecutableHash,
	})
	assertCode(t, err, apperr.CodeUnauthenticated)
}

func TestAuthenticate_MatchingProcessContext_IsAccepted(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	authenticated, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{
		PID:            registered.Agent.PID,
		ExecutableHash: registered.Agent.ExecutableHash,
	})
	if err != nil {
		t.Fatalf("进程上下文一致却被拒：%v", err)
	}
	if authenticated.ID != registered.Agent.ID {
		t.Errorf("认出的是 %s，期望 %s", authenticated.ID, registered.Agent.ID)
	}
}

func TestAuthenticate_TamperedType_DoesNotChangeTheResult(t *testing.T) {
	// REQ-AGENT-003 AC2：类型字段被篡改不影响身份校验，身份仍由哈希与密钥决定。
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	if _, err := h.db.Writer().ExecContext(h.ctx,
		"UPDATE agents SET type = 'generic', name = '别的名字' WHERE id = ?", registered.Agent.ID,
	); err != nil {
		t.Fatalf("篡改展示字段失败：%v", err)
	}

	authenticated, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{
		PID:            registered.Agent.PID,
		ExecutableHash: registered.Agent.ExecutableHash,
	})
	if err != nil {
		t.Fatalf("篡改展示字段后校验失败：%v", err)
	}
	if authenticated.ID != registered.Agent.ID {
		t.Errorf("认出的是 %s，期望 %s", authenticated.ID, registered.Agent.ID)
	}
}

func TestAuthenticate_DisconnectedAgent_IsRejected(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	if _, err := h.service.Disconnect(h.ctx, registered.Agent.ID); err != nil {
		t.Fatalf("断开会话失败：%v", err)
	}

	_, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{})
	assertCode(t, err, apperr.CodeUnauthenticated)
}

func TestAuthenticate_RecordsLastSeen(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	h.clock.Advance(5 * time.Minute)
	authenticated, err := h.service.Authenticate(h.ctx, registered.SessionKey, agentauth.Presented{})
	if err != nil {
		t.Fatalf("校验失败：%v", err)
	}

	expected := fixtures.Instant.Add(5 * time.Minute)
	if !authenticated.LastSeenAt.Equal(expected) {
		t.Errorf("最近活动时间为 %s，期望 %s", authenticated.LastSeenAt, expected)
	}
}

// ——— 信任等级（REQ-AGENT-002）———

func TestConfirmTrust_RaisesUnverifiedToKnown(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	confirmed, err := h.service.ConfirmTrust(h.ctx, registered.Agent.ID)
	if err != nil {
		t.Fatalf("确认信任失败：%v", err)
	}
	if confirmed.TrustLevel != agentauth.TrustKnown {
		t.Errorf("信任等级为 %s，期望 known", confirmed.TrustLevel)
	}

	// 重复确认不产生第二次副作用。推进时钟：时钟不动时再写一次也得到同样的
	// updated_at，断言就看不出区别了。
	h.clock.Advance(time.Minute)
	again, err := h.service.ConfirmTrust(h.ctx, registered.Agent.ID)
	if err != nil {
		t.Fatalf("重复确认失败：%v", err)
	}
	if !again.UpdatedAt.Equal(confirmed.UpdatedAt) {
		t.Error("重复确认改写了记录")
	}
}

func TestConfirmTrust_TrustedLevel_IsRefused(t *testing.T) {
	// trusted 保留给后续版本，本期没有任何入口写出它；读到它说明数据来自别处。
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	if _, err := h.db.Writer().ExecContext(h.ctx,
		"UPDATE agents SET trust_level = 'trusted' WHERE id = ?", registered.Agent.ID,
	); err != nil {
		t.Fatalf("写入 trusted 失败：%v", err)
	}

	_, err := h.service.ConfirmTrust(h.ctx, registered.Agent.ID)
	assertCode(t, err, apperr.CodeConflict)
}

func TestConfirmTrust_UnknownAgent_IsNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.service.ConfirmTrust(h.ctx, "01K1MISSING0000000000000000")
	assertCode(t, err, apperr.CodeNotFound)
}

// ——— Session Key 本身 ———

func TestSessionKey_NeverAppearsInAnyOutputPath(t *testing.T) {
	const plaintext = "SENTINEL_SESSION_KEY_d3adb33f"
	key := agentauth.NewSessionKey(plaintext)

	rendered := []string{
		fmt.Sprintf("%v", key),
		fmt.Sprintf("%s", key),
		fmt.Sprintf("%q", key),
		fmt.Sprintf("%#v", key),
		fmt.Sprintf("%x", key),
		fmt.Sprintf("%+v", struct{ Key agentauth.SessionKey }{Key: key}),
		key.String(),
	}

	encoded, err := json.Marshal(struct {
		Key agentauth.SessionKey `json:"session_key"`
	}{Key: key})
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	rendered = append(rendered, string(encoded), key.LogValue().String())

	for index, text := range rendered {
		if strings.Contains(text, plaintext) {
			t.Errorf("第 %d 条输出路径泄漏了明文：%s", index, text)
		}
	}

	if key.Reveal() != plaintext {
		t.Error("Reveal 取不回明文，接入面就没法把凭证交给 Agent")
	}
}

func TestRegister_SessionKeyIsStoredOnlyAsAHash(t *testing.T) {
	h := newHarness(t)
	registered := mustRegister(t, h, fixtures.Registration())

	plaintext := registered.SessionKey.Reveal()
	if plaintext == "" {
		t.Fatal("注册没有签发 Session Key")
	}
	if registered.Agent.SessionKeyHash == plaintext {
		t.Fatal("库里存的就是明文")
	}
	if registered.Agent.SessionKeyHash != agentauth.HashSessionKey(registered.SessionKey) {
		t.Error("存下的不是这把密钥的哈希")
	}

	var stored string
	if err := h.db.Reader().QueryRowContext(h.ctx,
		"SELECT session_key_hash FROM agents WHERE id = ?", registered.Agent.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("读取会话密钥列失败：%v", err)
	}
	if strings.Contains(stored, plaintext) {
		t.Error("会话密钥列中出现了明文")
	}
}

func TestRegister_TwoRegistrations_GetDifferentSessionKeys(t *testing.T) {
	h := newHarness(t)

	first := mustRegister(t, h, fixtures.Registration())
	h.clock.Advance(time.Minute)
	second := mustRegister(t, h, fixtures.Registration(func(r *agentauth.Registration) {
		r.PID = 5678
	}))

	if first.SessionKey.Reveal() == second.SessionKey.Reveal() {
		t.Fatal("两次注册签发了同一把 Session Key")
	}
}

func TestRegister_EntropyFailure_RefusesToIssueAKey(t *testing.T) {
	// 熵源坏了就签不出足够随机的密钥，此时必须拒绝而不是凑合发一把。
	h := newHarness(t, func(options *agentauth.Options) {
		options.Entropy = failingReader{}
	})

	_, err := h.service.Register(h.ctx, fixtures.Registration())
	assertCode(t, err, apperr.CodeInternal)

	if count := countRows(t, h.db, "agents"); count != 0 {
		t.Errorf("签发失败却留下了 %d 条 Agent 记录", count)
	}
}

func TestRegister_ShortEntropy_RefusesToIssueAKey(t *testing.T) {
	// 熵源只给出半截也不行：io.ReadFull 必须读满，短读等于密钥强度减半。
	h := newHarness(t, func(options *agentauth.Options) {
		options.Entropy = bytes.NewReader(make([]byte, 8))
	})

	_, err := h.service.Register(h.ctx, fixtures.Registration())
	assertCode(t, err, apperr.CodeInternal)
}

// ——— 构造 ———

func TestNewService_MissingDependency_IsRejected(t *testing.T) {
	db := fixtures.MigratedDB(t)
	moment := clock.NewFixed(fixtures.Instant)
	complete := agentauth.Options{
		Agents:     repo.NewAgents(db),
		Devices:    repo.NewDevices(db),
		Workspaces: repo.NewWorkspaces(db),
		Clock:      moment,
		IDs:        ulid.New(moment),
	}

	cases := []struct {
		name  string
		blank func(*agentauth.Options)
	}{
		{"缺 Agent 仓储", func(o *agentauth.Options) { o.Agents = nil }},
		{"缺设备仓储", func(o *agentauth.Options) { o.Devices = nil }},
		{"缺工作区仓储", func(o *agentauth.Options) { o.Workspaces = nil }},
		{"缺时钟", func(o *agentauth.Options) { o.Clock = nil }},
		{"缺 ID 生成器", func(o *agentauth.Options) { o.IDs = nil }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := complete
			testCase.blank(&options)

			_, err := agentauth.NewService(options)
			assertCode(t, err, apperr.CodeInvalidConfiguration)
		})
	}

	if _, err := agentauth.NewService(complete); err != nil {
		t.Fatalf("依赖齐全却构造失败：%v", err)
	}
}

func TestNewService_ZeroSessionTTL_FallsBackToTheDefault(t *testing.T) {
	h := newHarness(t, func(options *agentauth.Options) { options.SessionTTL = 0 })

	registered := mustRegister(t, h, fixtures.Registration())

	expected := fixtures.Instant.Add(agentauth.DefaultSessionTTL)
	if !registered.Agent.SessionExpiresAt.Equal(expected) {
		t.Errorf("过期时刻为 %s，期望 %s", registered.Agent.SessionExpiresAt, expected)
	}
}

func TestDefaultSessionTTL_IsOneHour(t *testing.T) {
	// 写死字面量：拿常量和自己比是同义反复，改坏了也发现不了。
	if agentauth.DefaultSessionTTL != time.Hour {
		t.Errorf("默认会话有效期为 %s，期望 1 小时", agentauth.DefaultSessionTTL)
	}
}

// ——— 辅助 ———

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func countRows(t *testing.T, db *store.DB, table string) int {
	t.Helper()

	var count int
	// table 只来自本文件的字面量，不含外部输入。
	statement := "SELECT COUNT(*) FROM " + table //nolint:gosec // 见上
	if err := db.Reader().QueryRowContext(t.Context(), statement).Scan(&count); err != nil {
		t.Fatalf("统计 %s 失败：%v", table, err)
	}
	return count
}
