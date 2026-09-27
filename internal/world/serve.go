// serve.go — 进程边界: JSONL 行协议循环与退出码(契约 §1)。
//
// 一段会话的形状: 一行请求 → 一行响应 → 按到达顺序一一对应, 不跨行、不批量、不并发
// 复用。stdout 上只有协议行; 任何人类日志都由调用方写到 stderr。
//
// 退出码: 0 正常(收到 close, 或 stdin EOF); 1 致命错误(已经回过 ok:false, 世界无法再
// 诚实服务); 2 用法错误(由 cmd/forge 在解析 flag 时给出)。
package world

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// Server 把一个 World 接到进程边界上。
type Server struct {
	W *World

	// Protocol 是本次会话请求的协议版本。它不等于 ProtocolVersion 时, 世界无法按
	// 客户端的语义诚实服务: 第一个请求会得到 unsupported_protocol, 随后进程以 1
	// 退出(契约 §3.5: 已回 ok:false 之后的致命错误)。
	Protocol int

	// Log 是人类日志的去处(一律 stderr)。
	Log io.Writer
}

// NewServer 构建一个会话。
func NewServer(w *World, protocol int, log io.Writer) *Server {
	return &Server{W: w, Protocol: protocol, Log: log}
}

// Supported 报告本次会话是否按一个已知的协议版本服务。
func (s *Server) Supported() bool { return s.Protocol == ProtocolVersion }

// answer 把一行请求变成一行响应。多个地方(replay 与 serve)共用它, 因此"世界怎么回应
// 一行字节"只有一条代码路径 —— 回放门验的就是这一条路径。
func (s *Server) answer(line []byte) (resp []byte, closed, fatal bool) {
	if !s.Supported() {
		return errLine(CodeUnsupportedProtocol,
				fmt.Sprintf("this world serves protocol %d only; the session asked for %d", ProtocolVersion, s.Protocol)),
			false, true
	}
	return s.W.Handle(line)
}

// Serve 跑一次会话: 从 r 一行一行读请求, 向 w 一行一行写响应, 直到 close 或 EOF。
//
// rec 非 nil 时, 每一对(原始请求行, 原始响应行)都会被追加进 trace(契约 §6)。
// 返回进程退出码。
func (s *Server) Serve(r io.Reader, w io.Writer, rec *Recorder) int {
	br := bufio.NewReaderSize(r, 1<<16)
	bw := bufio.NewWriter(w)

	for {
		line, readErr := br.ReadBytes('\n')
		// 原始请求字节 = 去掉那一个行尾换行符。空行是真实的请求(它会被判成 bad_json),
		// 因此这里只用 err 判断"没有下一行了", 不用长度。
		req := bytes.TrimSuffix(line, []byte("\n"))
		if len(req) == 0 && readErr != nil {
			return 0 // stdin EOF: 与 close 等价
		}

		resp, closed, fatal := s.answer(req)
		if err := writeLine(bw, resp); err != nil {
			fmt.Fprintf(s.logWriter(), "serve: cannot write a response: %v\n", err)
			return 1
		}
		if rec != nil {
			if err := rec.Write(req, resp); err != nil {
				fmt.Fprintf(s.logWriter(), "serve: cannot record the trace: %v\n", err)
				return 1
			}
		}
		if err := bw.Flush(); err != nil {
			fmt.Fprintf(s.logWriter(), "serve: cannot flush a response: %v\n", err)
			return 1
		}

		if closed {
			return 0
		}
		if fatal {
			return 1
		}
		if readErr != nil {
			return 0 // EOF: 这一行处理完了, 会话正常结束
		}
	}
}

func (s *Server) logWriter() io.Writer {
	if s.Log == nil {
		return io.Discard
	}
	return s.Log
}

// writeLine 写一行响应并补上换行。
func writeLine(w *bufio.Writer, resp []byte) error {
	if _, err := w.Write(resp); err != nil {
		return err
	}
	return w.WriteByte('\n')
}
