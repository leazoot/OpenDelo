package config

import (
	"log/slog"
	"net"
	"strconv"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// Config 是网关启动所必需的全部设置。
//
// 本包只覆盖「进程起来之前就得确定」的项。主题、语言、自动化等级这类偏好属于
// Preferences（REQ-PREF，S5/S6），它们可以热更新，不在这里。
type Config struct {
	// ListenAddress 是三个接入面共同的监听地址，默认只监听回环。
	ListenAddress string `json:"listen_address"`
	// WebAPIPort 是 Console 与 Web API 的端口（PRD §7.2）。
	WebAPIPort int `json:"web_api_port"`
	// AgentProxyPort 是 Agent Proxy 的端口。
	AgentProxyPort int `json:"agent_proxy_port"`
	// MCPPort 是 MCP over HTTP 的端口。
	MCPPort int `json:"mcp_port"`
	// LogLevel 取 debug / info / warn / error。任何级别都脱敏。
	LogLevel string `json:"log_level"`
	// SecurityLevel 是安全等级，取 L0 / L1（REQ-GATEWAY-005）。
	//
	// 默认 L1：MVP 的推荐模式，靠环境清理 + Proxy 拦截阻止受保护 Agent 直连
	// 受控服务。L0 只做代理与审计，不阻止 Agent 从别的出口出去 ——
	// 它适合初期测试，不适合放着不管（PRD §21）。
	// L2 Isolated 本期不实现，因此这里没有它的取值。
	SecurityLevel string `json:"security_level"`

	// AllowNonLoopback 是绑定非回环地址所需的显式确认。
	//
	// 它只放行「绑定哪个地址」这一件事。远程 Gateway 的传输与认证仍然未实现
	// （假设 A-04），打开它不会让远程连接可用。
	AllowNonLoopback bool `json:"allow_non_loopback"`

	// Dir 是本次加载所用的配置目录，由加载过程填入。
	Dir string `json:"-"`
}

// Default 返回全部默认值。PRD §7.2 规定了三个端口，默认只监听回环。
func Default() Config {
	return Config{
		ListenAddress:  "127.0.0.1",
		WebAPIPort:     8787,
		AgentProxyPort: 8788,
		MCPPort:        8789,
		LogLevel:       LevelInfo,
		SecurityLevel:  LevelEnforced,
	}
}

// 安全等级的取值（REQ-GATEWAY-005）。
const (
	// LevelRelay 是 L0：只代理与审计，不阻止 Agent 直连。
	LevelRelay = "L0"
	// LevelEnforced 是 L1：环境清理 + Proxy 拦截。MVP 默认。
	LevelEnforced = "L1"
)

// Enforced 报告当前是否处于 L1。
//
// 用方法而不是让调用方各自比较字符串：等级的判断出现在环境清理与 Proxy
// 两处，写成两个字符串比较迟早会有一处漏掉新增的取值。
func (c Config) Enforced() bool { return c.SecurityLevel == LevelEnforced }

// 日志级别的取值。
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// ParseLevel 把配置里的级别名转成 slog 级别。
func ParseLevel(level string) (slog.Level, bool) {
	switch level {
	case LevelDebug:
		return slog.LevelDebug, true
	case LevelInfo:
		return slog.LevelInfo, true
	case LevelWarn:
		return slog.LevelWarn, true
	case LevelError:
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// SlogLevel 返回已校验配置对应的 slog 级别。
func (c Config) SlogLevel() slog.Level {
	level, known := ParseLevel(c.LogLevel)
	if !known {
		return slog.LevelInfo
	}
	return level
}

// Overrides 是某一层显式给出的设置。nil 字段表示该层没有意见，交给下一层。
//
// 四层来源（命令行参数、环境变量、配置文件、默认值）用同一个类型表达，合并顺序
// 因此是一行 applyTo，不需要每层各写一遍字段判断。
type Overrides struct {
	ListenAddress    *string `json:"listen_address,omitempty"`
	WebAPIPort       *int    `json:"web_api_port,omitempty"`
	AgentProxyPort   *int    `json:"agent_proxy_port,omitempty"`
	MCPPort          *int    `json:"mcp_port,omitempty"`
	LogLevel         *string `json:"log_level,omitempty"`
	AllowNonLoopback *bool   `json:"allow_non_loopback,omitempty"`
}

func (o Overrides) applyTo(base Config) Config {
	applied := base
	if o.ListenAddress != nil {
		applied.ListenAddress = *o.ListenAddress
	}
	if o.WebAPIPort != nil {
		applied.WebAPIPort = *o.WebAPIPort
	}
	if o.AgentProxyPort != nil {
		applied.AgentProxyPort = *o.AgentProxyPort
	}
	if o.MCPPort != nil {
		applied.MCPPort = *o.MCPPort
	}
	if o.LogLevel != nil {
		applied.LogLevel = *o.LogLevel
	}
	if o.AllowNonLoopback != nil {
		applied.AllowNonLoopback = *o.AllowNonLoopback
	}
	return applied
}

// Validate 校验配置，不合法即拒绝启动。
//
// 校验在这里一次做完，其他包收到的 Config 已经是合法的，不必重复判断。
func (c Config) Validate() error {
	ports := map[string]int{
		"web_api_port":     c.WebAPIPort,
		"agent_proxy_port": c.AgentProxyPort,
		"mcp_port":         c.MCPPort,
	}
	used := make(map[int]string, len(ports))
	for _, name := range []string{"web_api_port", "agent_proxy_port", "mcp_port"} {
		port := ports[name]
		if port < 1 || port > 65535 {
			return invalidConfig(name + " 必须在 1–65535 之间，实际为 " + strconv.Itoa(port))
		}
		// 三个接入面的认证互相独立，共用端口会把这条边界抹掉。
		if other, taken := used[port]; taken {
			return invalidConfig(name + " 与 " + other + " 都是 " + strconv.Itoa(port) + "，三个接入面必须分开")
		}
		used[port] = name
	}

	if _, known := ParseLevel(c.LogLevel); !known {
		return invalidConfig("log_level 必须是 debug / info / warn / error，实际为 " + strconv.Quote(c.LogLevel))
	}

	// 认不出的等级一律拒绝，不当成 L0 也不当成 L1：一个把 security_level
	// 写成 "l1" 的用户，静默按 L0 跑起来就等于以为自己受着保护而其实没有。
	if c.SecurityLevel != LevelRelay && c.SecurityLevel != LevelEnforced {
		return invalidConfig("security_level 必须是 L0 或 L1，实际为 " + strconv.Quote(c.SecurityLevel))
	}

	return c.validateListenAddress()
}

func (c Config) validateListenAddress() error {
	address := net.ParseIP(c.ListenAddress)
	if address == nil {
		return invalidConfig("listen_address 不是合法的 IP 地址：" + strconv.Quote(c.ListenAddress))
	}
	if address.IsLoopback() {
		return nil
	}
	if !c.AllowNonLoopback {
		return invalidConfig(
			"listen_address " + c.ListenAddress + " 不是回环地址；非回环监听需要把 allow_non_loopback 显式设为 true",
		)
	}
	return nil
}

func invalidConfig(detail string) error {
	return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(detail)
}
