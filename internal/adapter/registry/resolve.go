package registry

import (
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 从一次出站请求反查「这是哪个已声明的操作」。
 *
 * Proxy 拿到的是方法与路径，而决策链路要的是服务与操作名。这段翻译只能放在
 * 声明的旁边：判断「/repos/x/y/issues 是不是 create_issue」需要路径模板，
 * 而模板是能力声明的一部分。放到 Proxy 里就等于让接入面自己维护第二份白名单，
 * 两份迟早不同 —— 而不同的那一次，出去的是一个没有被声明过的请求。
 *
 * 反查失败一律是「Adapter 未声明能力」，按 Fail Closed 拒绝。
 * 这里没有「猜一个最像的操作」的分支。
 */

// MatchPath 判断实际路径是否匹配模板，并取出模板中各占位段的取值。
//
// 模板形如 /repos/{owner}/{repo}/issues，返回 {"owner": ..., "repo": ...}。
// 段数不等、含 `..`、不以 / 开头、出现空段一律不匹配 —— 路径穿越在这里就被挡下，
// 而不是等到拼出 URL 之后。
func MatchPath(template, actual string) (map[string]string, bool) {
	if !strings.HasPrefix(actual, "/") || strings.Contains(actual, "..") {
		return nil, false
	}

	templateSegments := strings.Split(strings.TrimPrefix(template, "/"), "/")
	actualSegments := strings.Split(strings.TrimPrefix(actual, "/"), "/")
	if len(templateSegments) != len(actualSegments) {
		return nil, false
	}

	captured := make(map[string]string)
	for index, expected := range templateSegments {
		got := actualSegments[index]
		if got == "" {
			return nil, false
		}
		if strings.HasPrefix(expected, "{") && strings.HasSuffix(expected, "}") {
			captured[strings.TrimSuffix(strings.TrimPrefix(expected, "{"), "}")] = got
			continue
		}
		if expected != got {
			return nil, false
		}
	}
	return captured, true
}

// Resolve 把「服务 + 方法 + 路径」反查成一项已声明的能力与它的资源维度取值。
//
// 方法必须一致：同一条路径上的 GET 与 DELETE 是两个操作，风险等级也不同。
func (r *Registry) Resolve(service, method, path string) (Capability, map[string]string, error) {
	operations, found := r.capabilities[service]
	if !found {
		return Capability{}, nil, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("没有 Adapter 负责服务 " + service)
	}

	for _, capability := range operations {
		if !strings.EqualFold(capability.Method, method) {
			continue
		}
		if resource, matched := MatchPath(capability.Path, path); matched {
			return capability, resource, nil
		}
	}
	return Capability{}, nil, apperr.New(apperr.CodeCapabilityNotOffered).
		WithDetail(service + " 没有声明 " + strings.ToUpper(method) + " " + path)
}
