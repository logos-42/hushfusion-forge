// trace.go — 黄金 trace 的冻结格式、录制与逐字节回放(契约 §6)。
//
// trace 每行一个对象, req 与 resp 的值是**当时的原始字节**:
//
//	{"req":<原始请求行>,"resp":<原始响应行>}
//
// 实现方式必须是字符串拼接。把请求/响应重新序列化一遍再写进 trace 正是丢字节的地方:
// 一次 json.Marshal 往返会把 1e+12 变成 1000000000000, 把键序重排, 于是"逐字节复现"
// 的门就变成了在比较两个都不小心改过的字符串。因此这里的每一处都只用 []byte 拼接与
// 切片, 从不 Unmarshal 再 Marshal。
package world

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Recorder 把每一次 req/resp 追加进 trace 文件。
type Recorder struct {
	path string
	f    *os.File
	bw   *bufio.Writer
}

// NewRecorder 打开(创建/截断)一个 trace 文件。目录会自动创建。
func NewRecorder(path string) (*Recorder, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("world: cannot create %s: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("world: cannot create trace %s: %w", path, err)
	}
	return &Recorder{path: path, f: f, bw: bufio.NewWriter(f)}, nil
}

// Path 是这次录制写出的文件。
func (r *Recorder) Path() string { return r.path }

// Write 追加一行 trace: req/resp 是原始字节, 拼接而成(见本文件顶部)。
func (r *Recorder) Write(req, resp []byte) error {
	if bytes.ContainsAny(req, "\r\n") || bytes.ContainsAny(resp, "\r\n") {
		// 一行响应里出现换行会让 trace 变成两行, 而一行一个对象是冻结格式。
		return fmt.Errorf("world: refusing to record a message containing a line break (req=%q resp=%q)",
			clipLine(req), clipLine(resp))
	}
	buf := make([]byte, 0, len(req)+len(resp)+24)
	buf = append(buf, `{"req":`...)
	buf = append(buf, req...)
	buf = append(buf, `,"resp":`...)
	buf = append(buf, resp...)
	buf = append(buf, "}\n"...)
	if _, err := r.bw.Write(buf); err != nil {
		return err
	}
	return r.bw.Flush()
}

// Close 冲刷并关闭文件。
func (r *Recorder) Close() error {
	if err := r.bw.Flush(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}

// TraceLine 是 trace 里的一行。
type TraceLine struct {
	No   int    // 文件里的行号(从 1 开始), 报错时用它定位
	Req  []byte // 原始请求行
	Resp []byte // 原始响应行
}

// ReadTrace 读一个 trace 文件。
//
// 它用 json.RawMessage 取回两个字段的**原始字节**(Unmarshal 到 RawMessage 不会重新
// 序列化, 拿到的是输入里的那一段切片), 因此 req/resp 与写下来时逐字节相同。
func ReadTrace(path string) ([]TraceLine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("world: cannot read trace %s: %w", path, err)
	}
	// 文件末尾的那个换行是格式的一部分("一行一个对象"), 先摘掉它, 之后任何空行都是
	// 真实的损坏。
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	var out []TraceLine
	for i, rawLine := range bytes.Split(data, []byte("\n")) {
		no := i + 1
		line := bytes.TrimSuffix(rawLine, []byte("\r"))
		if len(line) == 0 {
			return nil, fmt.Errorf("world: trace %s line %d: empty line (the frozen format is one object per line)", path, no)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, fmt.Errorf("world: trace %s line %d: not a JSON object: %w", path, no, err)
		}
		if code, msg := rejectUnknown(fields, "req", "resp"); code != "" {
			return nil, fmt.Errorf("world: trace %s line %d: %s", path, no, msg)
		}
		req, okReq := fields["req"]
		resp, okResp := fields["resp"]
		if !okReq || !okResp {
			return nil, fmt.Errorf("world: trace %s line %d: needs both req and resp (got req=%v resp=%v)",
				path, no, okReq, okResp)
		}
		out = append(out, TraceLine{No: no, Req: []byte(req), Resp: []byte(resp)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("world: trace %s holds no records", path)
	}
	return out, nil
}

// TraceProtocol 读出 trace 里那条 hello 的**响应**声明的协议版本(§8.1: `--replay` 忽略
// 命令行/默认的 --protocol, 以 trace 自己的 hello 响应为准 —— 一份 v1 旧 trace 必须由
// 一个 v1 语义的世界回放, 否则"逐字节复现"验的就不是当初的那套语义)。
//
// 找不到 hello、或它的响应里没有整数 protocol, 都算这份 trace 不合格: 回放一个协议版本
// 说不清的会话只能靠猜, 而猜出来的 Pass 没有意义。
func TraceProtocol(path string) (int, error) {
	lines, err := ReadTrace(path)
	if err != nil {
		return 0, err
	}
	for _, ln := range lines {
		var req map[string]json.RawMessage
		if err := json.Unmarshal(ln.Req, &req); err != nil {
			continue // 畸形请求行是回放要报的错, 不是这里的错
		}
		rawOp, ok := req[OpKey]
		if !ok {
			continue
		}
		var op string
		if err := json.Unmarshal(rawOp, &op); err != nil || op != OpHello {
			continue
		}
		var resp map[string]json.RawMessage
		if err := json.Unmarshal(ln.Resp, &resp); err != nil {
			return 0, fmt.Errorf("world: trace %s line %d: the hello response is not a JSON object: %w", path, ln.No, err)
		}
		rawProto, ok := resp["protocol"]
		if !ok {
			return 0, fmt.Errorf("world: trace %s line %d: the hello response has no protocol field — "+
				"a trace must say which semantics it was recorded under", path, ln.No)
		}
		var proto int
		if err := json.Unmarshal(rawProto, &proto); err != nil {
			return 0, fmt.Errorf("world: trace %s line %d: the hello response's protocol is not an integer: %s",
				path, ln.No, clipLine(rawProto))
		}
		return proto, nil
	}
	return 0, fmt.Errorf("world: trace %s holds no hello request — a session must start with one", path)
}

// Replay 逐字节回放一个 trace(契约 §6 第 1 条): 把每条 req 喂给这个(新建的)世界,
// 产出的响应必须与 resp 完全相同。任一行不同即红, 并打印行号与第一处差异。
//
// 回放前先核对协议版本(§8.1): trace 声明 v1 而世界按 v2 构建时, 它不是"回放失败",
// 而是"回放的是另一套语义" —— 那种情况下报出来的差异一行都不可信, 所以这里直接拒绝。
//
// 报告走 stderr: stdout 只放协议行(契约 §1), 回放模式下一次会话都没有, 所以它一行
// 都不该往 stdout 写。
func (s *Server) Replay(path string, log io.Writer) int {
	lines, err := ReadTrace(path)
	if err != nil {
		fmt.Fprintf(log, "replay: %v\n", err)
		return 1
	}
	proto, err := TraceProtocol(path)
	if err != nil {
		fmt.Fprintf(log, "replay: %v\n", err)
		return 1
	}
	if proto != s.Protocol {
		fmt.Fprintf(log, "replay: %s was recorded under protocol %d but this world was built for protocol %d — "+
			"build the world from the trace's own protocol (§8.1) before replaying it\n", path, proto, s.Protocol)
		return 1
	}
	bad := 0
	closedAfter := -1
	for _, ln := range lines {
		if closedAfter >= 0 {
			fmt.Fprintf(log, "replay: line %d comes after close (line %d) — the trace cannot be a session\n",
				ln.No, closedAfter)
			return 1
		}
		got, closed, fatal := s.answer(ln.Req)
		if !bytes.Equal(got, ln.Resp) {
			bad++
			fmt.Fprintf(log, "replay: line %d MISMATCH (first difference at byte %d)\n",
				ln.No, firstDiff(got, ln.Resp))
			fmt.Fprintf(log, "         want: %s\n", clipLine(ln.Resp))
			fmt.Fprintf(log, "         got:  %s\n", clipLine(got))
		}
		if closed {
			closedAfter = ln.No
		}
		if fatal {
			fmt.Fprintf(log, "replay: line %d made the world fail internally; stopping\n", ln.No)
			return 1
		}
	}
	if bad != 0 {
		fmt.Fprintf(log, "replay: FAIL — %d of %d responses differ from the trace\n", bad, len(lines))
		return 1
	}
	fmt.Fprintf(log, "replay: PASS — %d requests, %d bytes of responses reproduced byte for byte\n",
		len(lines), traceBytes(lines))
	return 0
}

// firstDiff 返回两个字节串第一处不同的下标。
func firstDiff(got, want []byte) int {
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return n
}

func traceBytes(lines []TraceLine) int {
	n := 0
	for _, ln := range lines {
		n += len(ln.Resp)
	}
	return n
}
