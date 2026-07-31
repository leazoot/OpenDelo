package agentauth

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// DefaultSessionTTL 是 Session Key 的有效期。
//
// PRD §20 只说「短期身份」，没有给数字。取一小时：会话过期不会丢掉授权记忆
// （重新注册命中同一条 Agent 记录），所以短一点的代价只是接入面多握一次手，
// 而长一点的代价是一把泄漏的凭证多可用几个小时。不自动续期（REQ-AGENT-001）。
const DefaultSessionTTL = time.Hour

// ErrIdentityMismatch 标记 REQ-AGENT-001 异常状态中的第三种：同一 PID 提交了
// 与已注册记录不同的可执行文件哈希。
//
// 它作为 cause 挂在 apperr.CodeAgentIdentityUnverifiable 下 —— 对外只看到
// 「身份无法确认」，接入面则可以用 errors.Is 认出这一种，据此记 agent.identity_mismatch
// 审计事件。用哨兵错误而不是错误码，是因为对外不该区分「哈希读不到」与
// 「哈希对不上」，那等于告诉调用方它猜到了哪一步。
var ErrIdentityMismatch = errors.New("提交的可执行文件哈希与已注册记录不符")

// Options 是 Service 的依赖。除 Entropy 与 SessionTTL 外全部必填。
type Options struct {
	Agents     AgentRepository
	Devices    DeviceRepository
	Workspaces WorkspaceRepository
	Clock      clock.Clock
	IDs        *ulid.Generator
	// Entropy 为空时使用 crypto/rand。
	Entropy io.Reader
	// SessionTTL 为零或负时使用 DefaultSessionTTL。
	SessionTTL time.Duration
}

// Service 负责 Agent 注册、Session Key 签发与身份校验（REQ-AGENT-001~003）。
//
// 这里没有任何「列举 Agent」的方法：REQ-AGENT-001 的权限边界要求 Agent 只能
// 查询自己的会话状态。做不到的事最好在类型上就不存在。
type Service struct {
	agents     AgentRepository
	devices    DeviceRepository
	workspaces WorkspaceRepository
	clock      clock.Clock
	ids        *ulid.Generator
	entropy    io.Reader
	sessionTTL time.Duration
}

// NewService 校验依赖并构造服务。缺依赖时返回错误而不是等到运行期空指针。
func NewService(options Options) (*Service, error) {
	switch {
	case options.Agents == nil:
		return nil, configError("缺少 Agent 仓储")
	case options.Devices == nil:
		return nil, configError("缺少设备仓储")
	case options.Workspaces == nil:
		return nil, configError("缺少工作区仓储")
	case options.Clock == nil:
		return nil, configError("缺少时钟")
	case options.IDs == nil:
		return nil, configError("缺少 ID 生成器")
	}

	entropy := options.Entropy
	if entropy == nil {
		entropy = defaultEntropy
	}
	sessionTTL := options.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}

	return &Service{
		agents:     options.Agents,
		devices:    options.Devices,
		workspaces: options.Workspaces,
		clock:      options.Clock,
		ids:        options.IDs,
		entropy:    entropy,
		sessionTTL: sessionTTL,
	}, nil
}

func configError(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(detail)
}

// Registration 是握手时提交的进程上下文（REQ-AGENT-001 输入）。
//
// Name、Type 与 Version 只用于展示；能证明「是我启动的那个进程」的是其余字段
// （REQ-AGENT-003 AC2）。
type Registration struct {
	Name    string
	Type    AgentType
	Version string

	ExecutableHash string
	ExecutablePath string
	PID            int
	// ParentPID 为 0 表示没有父进程，这是允许的（REQ-AGENT-001 AC3）。
	ParentPID int
	OSUser    string

	DeviceFingerprint string
	DeviceName        string

	WorkspacePath      string
	ProjectFingerprint string

	StartedAt time.Time
}

// Registered 是一次注册的结果（REQ-AGENT-001 输出）。
type Registered struct {
	Agent Agent
	// SessionKey 是本次会话的凭证，只在这里出现一次：库里存的是它的哈希。
	SessionKey SessionKey
	// IsRebind 为真表示命中了同一身份的已有记录（重启），Agent.ID 与上次相同，
	// 绑定其上的授权记忆继续有效。为假表示新建了一条记录。
	IsRebind bool
	// ProjectFingerprintChanged 为真表示工作区还是那个路径，但项目指纹变了。
	// 依赖该项目的绑定需要重新确认（REQ-IDENT-003 AC3），失效由 core/trust 处理。
	ProjectFingerprintChanged bool
}

// Register 注册一个 Agent 并签发 Session Key。
//
// 命中同一身份绑定（设备 + 工作区 + 可执行文件路径 + 系统用户 + 可执行文件哈希）
// 时复用原记录并换发密钥，旧 Session Key 立即失效；可执行文件哈希变了则匹配不到，
// 于是新建一条记录，旧记忆随旧记录留在原地，新 Agent 查不到它们（REQ-AGENT-001 AC2）。
func (s *Service) Register(ctx context.Context, registration Registration) (Registered, error) {
	normalized, err := registration.normalize()
	if err != nil {
		return Registered{}, err
	}

	now := s.clock.Now()

	device, err := s.resolveDevice(ctx, normalized, now)
	if err != nil {
		return Registered{}, err
	}
	workspace, fingerprintChanged, err := s.resolveWorkspace(ctx, normalized, now)
	if err != nil {
		return Registered{}, err
	}

	key, err := generateSessionKey(s.entropy)
	if err != nil {
		return Registered{}, err
	}
	keyHash := HashSessionKey(key)
	expiresAt := now.Add(s.sessionTTL)

	existing, err := s.agents.AgentByBinding(ctx, Binding{
		DeviceID:       device.ID,
		WorkspaceID:    workspace.ID,
		ExecutablePath: normalized.ExecutablePath,
		OSUser:         normalized.OSUser,
		ExecutableHash: normalized.ExecutableHash,
	})
	switch {
	case err == nil:
		rebound, rebindErr := s.agents.RebindAgent(ctx, existing.ID, Rebind{
			PID:              normalized.PID,
			ParentPID:        normalized.ParentPID,
			StartedAt:        normalized.StartedAt,
			SessionKeyHash:   keyHash,
			SessionExpiresAt: expiresAt,
			At:               now,
		})
		if rebindErr != nil {
			return Registered{}, rebindErr
		}
		return Registered{
			Agent:                     rebound,
			SessionKey:                key,
			IsRebind:                  true,
			ProjectFingerprintChanged: fingerprintChanged,
		}, nil
	case apperr.Is(err, apperr.CodeNotFound):
		created, createErr := s.createAgent(ctx, normalized, device.ID, workspace.ID, keyHash, expiresAt, now)
		if createErr != nil {
			return Registered{}, createErr
		}
		return Registered{
			Agent:                     created,
			SessionKey:                key,
			ProjectFingerprintChanged: fingerprintChanged,
		}, nil
	default:
		return Registered{}, err
	}
}

func (s *Service) createAgent(
	ctx context.Context,
	registration Registration,
	deviceID, workspaceID, keyHash string,
	expiresAt, now time.Time,
) (Agent, error) {
	id, err := s.ids.NewID()
	if err != nil {
		return Agent{}, apperr.Wrap(apperr.CodeInternal, err).WithDetail("生成 Agent 主键失败")
	}

	return s.agents.CreateAgent(ctx, Agent{
		ID:             id,
		Name:           registration.Name,
		Type:           registration.Type,
		Version:        registration.Version,
		ExecutableHash: registration.ExecutableHash,
		ExecutablePath: registration.ExecutablePath,
		PID:            registration.PID,
		ParentPID:      registration.ParentPID,
		OSUser:         registration.OSUser,
		DeviceID:       deviceID,
		WorkspaceID:    workspaceID,
		StartedAt:      registration.StartedAt,
		SessionKeyHash: keyHash,

		SessionExpiresAt: expiresAt,
		// 新注册一律 unverified，用户在 Identities 页面确认后才升级（REQ-AGENT-002 AC1）。
		TrustLevel: TrustUnverified,
		Status:     StatusActive,
		LastSeenAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

func (s *Service) resolveDevice(ctx context.Context, registration Registration, now time.Time) (Device, error) {
	device, err := s.devices.DeviceByFingerprint(ctx, registration.DeviceFingerprint)
	if err == nil {
		return device, nil
	}
	if !apperr.Is(err, apperr.CodeNotFound) {
		return Device{}, err
	}

	id, err := s.ids.NewID()
	if err != nil {
		return Device{}, apperr.Wrap(apperr.CodeInternal, err).WithDetail("生成设备主键失败")
	}
	return s.devices.CreateDevice(ctx, Device{
		ID:          id,
		Fingerprint: registration.DeviceFingerprint,
		Name:        registration.DeviceName,
		// 第一次见到的设备默认不可信，由用户确认后才改变（Fail Closed）。
		TrustStatus: DeviceUntrusted,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

// resolveWorkspace 找出或建立工作区，并报告项目指纹是否发生了变化。
func (s *Service) resolveWorkspace(
	ctx context.Context, registration Registration, now time.Time,
) (Workspace, bool, error) {
	workspace, err := s.workspaces.WorkspaceByPath(ctx, registration.WorkspacePath)
	switch {
	case err == nil:
		if workspace.ProjectFingerprint == registration.ProjectFingerprint {
			return workspace, false, nil
		}
		updated, updateErr := s.workspaces.SetWorkspaceFingerprint(
			ctx, workspace.ID, registration.ProjectFingerprint, now,
		)
		if updateErr != nil {
			return Workspace{}, false, updateErr
		}
		return updated, true, nil
	case !apperr.Is(err, apperr.CodeNotFound):
		return Workspace{}, false, err
	}

	id, idErr := s.ids.NewID()
	if idErr != nil {
		return Workspace{}, false, apperr.Wrap(apperr.CodeInternal, idErr).WithDetail("生成工作区主键失败")
	}
	created, createErr := s.workspaces.CreateWorkspace(ctx, Workspace{
		ID:                 id,
		Path:               registration.WorkspacePath,
		ProjectFingerprint: registration.ProjectFingerprint,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if createErr != nil {
		return Workspace{}, false, createErr
	}
	return created, false, nil
}

// Presented 是随请求一并提交的进程上下文，用于确认「还是当初注册的那个进程吗」。
//
// 零值表示接入面无法获知进程上下文（例如 HTTP Proxy 的后续请求）。此时跳过这一层
// 校验，但不放宽任何其他检查：Session Key、状态与有效期照常。
type Presented struct {
	PID            int
	ExecutableHash string
}

// Authenticate 用 Session Key 认出 Agent。
//
// 不接受 agent_id 参数：身份完全由密钥决定，因此伪造 agent_id 无从谈起
// （REQ-AGENT-001 AC1）。任何一步不确定都返回错误，不存在「先放过再说」的分支。
func (s *Service) Authenticate(ctx context.Context, key SessionKey, presented Presented) (Agent, error) {
	if key.IsEmpty() {
		return Agent{}, unauthenticated("请求未携带 Session Key")
	}

	keyHash := HashSessionKey(key)
	agent, err := s.agents.AgentBySessionKeyHash(ctx, keyHash)
	if err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			return Agent{}, unauthenticated("Session Key 无效")
		}
		return Agent{}, err
	}
	if !sameHash(agent.SessionKeyHash, keyHash) {
		return Agent{}, unauthenticated("Session Key 无效")
	}
	if agent.Status != StatusActive {
		return Agent{}, unauthenticated("该会话已断开")
	}
	if !agent.SessionExpiresAt.After(s.clock.Now()) {
		// 不自动续期：过期就必须重新认证（REQ-AGENT-001 异常状态）。
		return Agent{}, apperr.New(apperr.CodeSessionExpired).WithDetail("Session Key 已过期")
	}

	if presented.PID != 0 && presented.PID != agent.PID {
		return Agent{}, unauthenticated("Session Key 由另一个进程提交")
	}
	if presented.ExecutableHash != "" && presented.ExecutableHash != agent.ExecutableHash {
		return Agent{}, apperr.Wrap(apperr.CodeAgentIdentityUnverifiable, ErrIdentityMismatch).
			WithDetail("可执行文件哈希与注册时不符")
	}

	return s.agents.TouchAgent(ctx, agent.ID, s.clock.Now())
}

// ConfirmTrust 把 Agent 升为 known（REQ-AGENT-002 AC3）。
//
// 只有 unverified → known 一条路径。trusted 保留给后续版本，本期没有任何入口
// 能写出它；读到 trusted 说明数据来自别处，此时拒绝而不是继续。
func (s *Service) ConfirmTrust(ctx context.Context, agentID string) (Agent, error) {
	agent, err := s.agents.AgentByID(ctx, agentID)
	if err != nil {
		return Agent{}, err
	}

	switch agent.TrustLevel {
	case TrustKnown:
		// 重复确认返回首次的结果，不产生第二次副作用。
		return agent, nil
	case TrustUnverified:
		return s.agents.SetAgentTrustLevel(ctx, agentID, TrustKnown, s.clock.Now())
	case TrustTrusted:
		return Agent{}, apperr.New(apperr.CodeConflict).WithDetail("该 Agent 的信任等级本期不可变更")
	default:
		return Agent{}, apperr.New(apperr.CodeInternal).WithDetail("Agent 的信任等级取值非法")
	}
}

// Disconnect 标记会话结束。此后该 Session Key 立即失效，即便尚未到期。
func (s *Service) Disconnect(ctx context.Context, agentID string) (Agent, error) {
	return s.agents.SetAgentStatus(ctx, agentID, StatusDisconnected, s.clock.Now())
}

func unauthenticated(detail string) error {
	return apperr.New(apperr.CodeUnauthenticated).WithDetail(detail)
}

// normalize 校验注册请求并返回规范化后的副本。
//
// 身份绑定缺任何一项都返回 agent_identity_unverifiable：缺了就无法确认这是谁，
// 而不确定一律拒绝。只提供 name 的请求因此被拒（REQ-AGENT-001 AC4）。
func (r Registration) normalize() (Registration, error) {
	// 先判身份绑定，再判展示字段：只带 name 的请求首先是「认不出这是谁」，
	// 回一个「少了个字段」会让调用方以为补上名字就能过（AC4）。
	//
	// 可执行文件哈希单独先判：REQ-AGENT-001 异常状态点名了这一种。
	if r.ExecutableHash == "" {
		return Registration{}, unverifiable("无法读取可执行文件哈希")
	}
	switch {
	case r.ExecutablePath == "":
		return Registration{}, unverifiable("缺少可执行文件路径")
	case r.PID <= 0:
		return Registration{}, unverifiable("缺少进程号")
	case r.ParentPID < 0:
		return Registration{}, unverifiable("父进程号非法")
	case r.OSUser == "":
		return Registration{}, unverifiable("缺少系统用户")
	case r.DeviceFingerprint == "":
		return Registration{}, unverifiable("缺少设备指纹")
	case r.WorkspacePath == "":
		return Registration{}, unverifiable("缺少工作区路径")
	case r.ProjectFingerprint == "":
		return Registration{}, unverifiable("缺少项目指纹")
	case r.StartedAt.IsZero():
		return Registration{}, unverifiable("缺少进程启动时间")
	}

	switch {
	case r.Name == "":
		return Registration{}, apperr.New(apperr.CodeInvalidRequest).WithDetail("缺少 Agent 名称")
	case !r.Type.valid():
		return Registration{}, apperr.New(apperr.CodeInvalidRequest).WithDetail("Agent 类型不在支持的五种之内")
	case r.DeviceName == "":
		return Registration{}, apperr.New(apperr.CodeInvalidRequest).WithDetail("缺少设备名称")
	}

	executablePath, err := absolutePath(r.ExecutablePath, "可执行文件路径")
	if err != nil {
		return Registration{}, err
	}
	workspacePath, err := absolutePath(r.WorkspacePath, "工作区路径")
	if err != nil {
		return Registration{}, err
	}

	normalized := r
	normalized.ExecutablePath = executablePath
	normalized.WorkspacePath = workspacePath
	normalized.StartedAt = r.StartedAt.UTC()
	return normalized, nil
}

// absolutePath 规范化路径并要求它是绝对路径。
//
// 规范化消掉 ".." 与重复分隔符，同时保证
// 同一个目录只会对应一条工作区记录 —— 否则 /a/b 与 /a/./b 会被当成两个项目，
// 授权也就被切成两份。
func absolutePath(raw, label string) (string, error) {
	cleaned := filepath.Clean(raw)
	if !filepath.IsAbs(cleaned) {
		return "", unverifiable(label + "不是绝对路径")
	}
	return cleaned, nil
}

func unverifiable(detail string) error {
	return apperr.New(apperr.CodeAgentIdentityUnverifiable).WithDetail(detail)
}

func (t AgentType) valid() bool {
	switch t {
	case TypeClaudeCode, TypeCodex, TypeGeminiCLI, TypeOpenCode, TypeGeneric:
		return true
	default:
		return false
	}
}
