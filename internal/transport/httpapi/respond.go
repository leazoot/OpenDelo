package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

const (
	headerContentType = "Content-Type"
	headerAllow       = "Allow"
	contentTypeJSON   = "application/json; charset=utf-8"
)

// errorEnvelope 是 REQ-API-003 规定的错误响应体：{"error":{code,message,operation_id}}。
// 所有错误响应都必须经这里写出，不允许出现裸文本的错误正文。
type errorEnvelope struct {
	Error apperr.Public `json:"error"`
}

func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, body any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 响应头已经发出，状态码改不了，只能记录。正文不进日志。
		logger.ErrorContext(r.Context(), "写出响应失败", slog.String("error", err.Error()))
	}
}

// validationEnvelope 是校验失败时的响应体。
//
// error 对象保持 REQ-API-003 的形状不变，出错的字段名放在同级的 fields 里：
// REQ-CAP-001 AC1 要求错误体里出现缺失的字段名，而 apperr 的对外 message
// 只能取自码表（那正是凭据不会顺着错误路径外泄的原因），两条要求只能这样同时满足。
// 往 error 对象里加键会让「错误体长什么样」出现第二种形状。
type validationEnvelope struct {
	Error  apperr.Public `json:"error"`
	Fields []string      `json:"fields"`
}

// writeValidationError 写出一次带字段名的 400。
//
// fields 里只允许放**本端点自己定义的字段名**，不放用户提交的取值 ——
// 回显请求内容会把一条可控字符串带进 Console 的渲染路径。
func writeValidationError(
	w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, fields ...string,
) {
	writeJSON(w, r, logger, http.StatusBadRequest, validationEnvelope{
		Error:  apperr.PublicOf(err, logging.OperationIDFrom(r.Context())),
		Fields: fields,
	})
}

// writeError 把错误折叠成对外表示后写出。
//
// message 只能来自 apperr 的码表，驱动与标准库的错误文本不会顺着这条路径外泄。
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, err error) {
	public := apperr.PublicOf(err, logging.OperationIDFrom(r.Context()))
	writeJSON(w, r, logger, status, errorEnvelope{Error: public})
}

// allowMethods 只放行列出的方法，其余返回 405。
//
// 自己做方法校验而不是交给 ServeMux 的模式匹配，是因为 ServeMux 的 405 正文是裸文本，
// 与 REQ-API-003 的错误契约不一致。
func allowMethods(logger *slog.Logger, next http.Handler, methods ...string) http.Handler {
	allowed := strings.Join(methods, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, method := range methods {
			if r.Method == method {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set(headerAllow, allowed)
		writeError(w, r, logger, http.StatusMethodNotAllowed,
			apperr.New(apperr.CodeInvalidRequest).WithDetail("方法 "+r.Method+" 不被支持，允许的方法："+allowed))
	})
}
