package mcpsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
)

/*
 * stdio 传输（REQ-MCP-003）。
 *
 * 实测两个客户端都用换行分隔的 JSON，没有 Content-Length 分帧，
 * 因此逐行读写即可。
 */

// stdioBuffer 是单行上限。一条 tools/call 的参数不会接近这个数量级，
// 超过它说明对面在灌数据，读到就断开而不是无限增长。
const stdioBuffer = 1 << 20

// ServeStdio 在一对读写端上跑完一次会话，直到输入结束或 ctx 取消。
//
// sessionKey 由调用方从环境里取出后传进来：stdio 上没有请求头可用，
// Agent 在注册时拿到的密钥经环境变量交给自己启动的网关进程。
// 密钥不经命令行参数。
func ServeStdio(
	ctx context.Context, server *Server, input io.Reader, output io.Writer, sessionKey string,
) error {
	reader := bufio.NewReaderSize(input, stdioBuffer)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), stdioBuffer)

	writer := bufio.NewWriter(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		incoming, ok := decodeFrame(scanner.Text())
		if !ok {
			// 解不开的帧没有 id，按 JSON-RPC 用 null 回应。
			if err := writeLine(writer, newError(nil, codeParseError,
				"request is not valid JSON")); err != nil {
				return err
			}
			continue
		}

		reply, handled := server.Dispatch(ctx, incoming, sessionKey)
		if !handled {
			continue
		}
		if err := writeLine(writer, reply); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// writeLine 写出一条回应并立刻刷出。
//
// 每条都刷：对面是一个在等回应的进程，攒在缓冲区里等于挂起它。
func writeLine(writer *bufio.Writer, reply response) error {
	encoded, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}
