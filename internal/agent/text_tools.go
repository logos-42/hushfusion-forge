package agent

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// 有些 OpenAI-compatible 端点不会返回原生 tool_calls，而是把调用写成
// Anthropic 风格的文本块：
//
//	<invoke name="mem.get">
//	<parameter name="key">x</parameter>
//	</invoke>
//
// 这里做 fail-closed 的文本兜底：只识别已注册工具，去掉调用块，再交给
// 与原生 tool_calls 完全相同的执行/回填循环。未知名字保留原文，不执行。

var invokeBlockRE = regexp.MustCompile(`(?s)<invoke\s+name\s*=\s*["']([^"']+)["']\s*>(.*?)</invoke>`)
var invokeParamRE = regexp.MustCompile(`(?s)<parameter\s+name\s*=\s*["']([^"']+)["']\s*>(.*?)</parameter>`)

func parseInvokeArgs(inner string) (string, bool) {
	inner = strings.TrimSpace(html.UnescapeString(inner))
	if inner == "" {
		return "{}", true
	}
	if ms := invokeParamRE.FindAllStringSubmatch(inner, -1); len(ms) > 0 {
		obj := make(map[string]any, len(ms))
		for _, m := range ms {
			obj[strings.TrimSpace(m[1])] = strings.TrimSpace(html.UnescapeString(m[2]))
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(inner), &obj); err == nil {
		b, err := json.Marshal(obj)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// parseTextToolCalls 返回（去掉已知工具调用块后的文本，调用列表）。
// 未知工具名不删、不执行，避免把代码示例误当工具调用。
func parseTextToolCalls(content string, known map[string]bool) (string, []ai.ToolCall) {
	locs := invokeBlockRE.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return content, nil
	}
	var calls []ai.ToolCall
	seen := map[string]bool{}
	type span struct{ start, end int }
	var strips []span
	for i, loc := range locs {
		name := content[loc[2]:loc[3]]
		if !known[name] {
			continue
		}
		args, ok := parseInvokeArgs(content[loc[4]:loc[5]])
		if !ok {
			continue
		}
		strips = append(strips, span{loc[0], loc[1]})
		key := name + "\x00" + args
		if seen[key] {
			continue // 重复调用只执行一次
		}
		seen[key] = true
		var call ai.ToolCall
		call.ID = fmt.Sprintf("text-%d-%d", time.Now().UnixNano(), i)
		call.Type = "function"
		call.Function.Name = name
		call.Function.Arguments = args
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return content, nil
	}
	var b strings.Builder
	last := 0
	for _, s := range strips {
		if s.start > last {
			b.WriteString(content[last:s.start])
		}
		last = s.end
	}
	if last < len(content) {
		b.WriteString(content[last:])
	}
	return strings.TrimSpace(b.String()), calls
}

// textToolStreamFilter 在 SSE 增量阶段隐藏 <invoke>…</invoke> 块，
// 避免用户先看到一屏原始调用文本；正常内容仍逐字透传。
type textToolStreamFilter struct {
	on       func(ai.StreamEvent)
	buf      strings.Builder
	inInvoke bool
}

func newTextToolStreamFilter(on func(ai.StreamEvent)) *textToolStreamFilter {
	return &textToolStreamFilter{on: on}
}

// adoptTextToolCalls 在模型没有返回原生 tool_calls 时，把文本里的
// <invoke> 块转成标准调用；只接受当前 registry 里存在的工具。
func (s *Session) adoptTextToolCalls(resp *ai.ChatResponse) {
	if resp == nil || len(resp.ToolCalls) > 0 {
		return
	}
	known := map[string]bool{}
	for _, n := range s.registry.Names() {
		known[n] = true
		known[safeToolName(n)] = true // 模型可能使用 provider 安全名
	}
	clean, calls := parseTextToolCalls(resp.Content, known)
	if len(calls) == 0 {
		return
	}
	if s.cfg.MaxToolResults > 0 && len(calls) > s.cfg.MaxToolResults {
		calls = calls[:s.cfg.MaxToolResults]
	}
	for i := range calls {
		if orig, ok := s.registry.ResolveName(calls[i].Function.Name); ok {
			calls[i].Function.Name = orig
		}
	}
	resp.Content = clean
	resp.ToolCalls = calls
}

func (f *textToolStreamFilter) handle(ev ai.StreamEvent) {
	if ev.Type != ai.StreamContent || ev.Text == "" {
		f.on(ev)
		return
	}
	f.buf.WriteString(ev.Text)
	text := f.buf.String()
	for {
		if f.inInvoke {
			idx := strings.Index(text, "</invoke>")
			if idx < 0 {
				f.buf.Reset()
				f.buf.WriteString(text)
				return
			}
			text = text[idx+len("</invoke>"):]
			f.inInvoke = false
			continue
		}
		idx := strings.Index(text, "<invoke")
		if idx < 0 {
			keep := len(text)
			if i := strings.LastIndex(text, "<"); i >= 0 && len(text)-i <= 8 {
				keep = i // 可能是被切开的 "<invoke"
			}
			if keep > 0 {
				f.on(ai.StreamEvent{Type: ai.StreamContent, Text: text[:keep]})
			}
			f.buf.Reset()
			f.buf.WriteString(text[keep:])
			return
		}
		if idx > 0 {
			f.on(ai.StreamEvent{Type: ai.StreamContent, Text: text[:idx]})
		}
		text = text[idx:]
		if end := strings.Index(text, "</invoke>"); end >= 0 {
			text = text[end+len("</invoke>"):]
			continue
		}
		f.inInvoke = true
		f.buf.Reset()
		f.buf.WriteString(text)
		return
	}
}

func (f *textToolStreamFilter) flush() {
	if !f.inInvoke && f.buf.Len() > 0 {
		f.on(ai.StreamEvent{Type: ai.StreamContent, Text: f.buf.String()})
	}
	f.buf.Reset()
}
