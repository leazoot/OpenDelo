package apperr

import "errors"

// Error 是本产品的统一错误类型。
//
// 它把「对外可见」与「仅供诊断」两部分明确分开：
//   - 对外：Code、码表里的固定 message、OperationID —— 见 Public。
//   - 对内：detail 与 cause 链，只出现在 Error() 的字符串里，供日志与排查使用。
//
// 因为对外 message 只能取自码表，任何写进 detail 或 cause 的路径、主机名、请求内容
// 都不可能被序列化到 API 响应里。
type Error struct {
	code        Code
	operationID string
	detail      string
	cause       error
}

// Public 是错误的对外表示，对应 REQ-API-003 的 `error` 对象。
// HTTP 层的信封由 transport 负责（S5），本包不做序列化以外的约定。
type Public struct {
	Code        Code   `json:"code"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id"`
}

// New 用给定码构造错误。
//
// 传入零值 Code（意味着调用方漏写了码）时规整为 CodeInternal —— 未知一律按最保守的
// 情况对待，而不是让一个空码流到响应里。
func New(code Code) *Error {
	if !code.Valid() {
		code = CodeInternal
	}
	return &Error{code: code}
}

// Wrap 用给定码包装 cause，保留错误链供 errors.Is 与 errors.As 使用。
// cause 只进入诊断路径，绝不出现在 Public 中。
func Wrap(code Code, cause error) *Error {
	wrapped := New(code)
	wrapped.cause = cause
	return wrapped
}

// WithOperationID 返回带操作 ID 的副本。操作 ID 是外部可见的，它是用户在账本中
// 定位这次请求的唯一线索（REQ-API-003 AC3）。
func (e *Error) WithOperationID(operationID string) *Error {
	copied := *e
	copied.operationID = operationID
	return &copied
}

// WithDetail 返回带诊断信息的副本。
//
// detail 只进日志与 Error()，永远不进 Public。即便如此也不得写入凭据：
// 明文一律走 secret.Value。
func (e *Error) WithDetail(detail string) *Error {
	copied := *e
	copied.detail = detail
	return &copied
}

func (e *Error) Code() Code { return e.code }

// OperationID 返回操作 ID，未设置时为空字符串。
func (e *Error) OperationID() string { return e.operationID }

// Public 返回对外表示。message 取自码表，与 detail 和 cause 无关。
func (e *Error) Public() Public {
	return Public{
		Code:        e.code,
		Message:     messages[e.code],
		OperationID: e.operationID,
	}
}

// Error 实现 error。这条字符串是诊断用的，含 detail 与 cause；对外一律用 Public。
func (e *Error) Error() string {
	text := e.code.String() + ": " + messages[e.code]
	if e.operationID != "" {
		text += " (operation_id=" + e.operationID + ")"
	}
	if e.detail != "" {
		text += ": " + e.detail
	}
	if e.cause != nil {
		text += ": " + e.cause.Error()
	}
	return text
}

// Unwrap 暴露 cause，使 errors.Is 与 errors.As 能沿链继续。
func (e *Error) Unwrap() error { return e.cause }

// Is 让 errors.Is 按错误码比较，而不是按指针相等。
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e.code == other.code
}

// Is 报告 err 链上是否存在带该码的 *Error。
//
// 走 errors.Is 而不是 errors.As：后者只取链上第一个 *Error，被包装过的码就查不到了。
func Is(err error, code Code) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, New(code))
}

// CodeOf 返回 err 链上第一个 *Error 的码。
//
// err 为 nil 时返回零值 Code；err 非 nil 但不是 *Error 时返回 CodeInternal ——
// 无法归类的错误按最保守的情况对待，不存在「不确定就当没错」。
func CodeOf(err error) Code {
	if err == nil {
		return Code{}
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.code
	}
	return CodeInternal
}

// PublicOf 把任意 error 转成对外表示。
//
// 这是错误离开进程前的唯一出口：不是 *Error 的错误（驱动、标准库、第三方）
// 一律折叠成 internal，它们的文本可能含连接串、路径或请求内容，绝不外泄。
// fallbackOperationID 在错误自身没带操作 ID 时补上，使 500 也能被定位
// （REQ-API-003 AC3）。
func PublicOf(err error, fallbackOperationID string) Public {
	var appErr *Error
	if !errors.As(err, &appErr) {
		return Public{
			Code:        CodeInternal,
			Message:     messages[CodeInternal],
			OperationID: fallbackOperationID,
		}
	}

	public := appErr.Public()
	if public.OperationID == "" {
		public.OperationID = fallbackOperationID
	}
	return public
}
