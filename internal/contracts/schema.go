package contracts

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// schemaFS 内嵌 schema 文件。
//
// 内嵌（而不是从磁盘读）有三个理由:
//  1. 校验器必须能从**任何**工作目录调用（测试、CLI、未来的硬件代理都在不同目录）；
//  2. schema 与类型同仓同包, 一次提交原子生效, 不会出现"类型改了 schema 忘了改"；
//  3. 二进制不含外部文件依赖, 便于将来拆成独立模块。
//
//go:embed schema/*.schema.json
var schemaFS embed.FS

// SchemaFile 是 (类型名 → schema 文件名) 的映射。
var SchemaFile = map[string]string{
	"DesignCandidate": "design-candidate.schema.json",
	"DesignVerdict":   "design-verdict.schema.json",
	"DeviceState":     "device-state.schema.json",
	"ControlCommand":  "control-command.schema.json",
	"Experiment":      "experiment.schema.json",
	"SafetyDecision":  "safety-decision.schema.json",
}

// SchemaPath 返回 schema 文件在嵌入文件系统里的路径。
func SchemaPath(name string) string {
	return "schema/" + name
}

// LoadSchema 读一个 schema 文件。
func LoadSchema(name string) (map[string]any, error) {
	b, err := schemaFS.ReadFile(SchemaPath(name))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("schema %s 不是合法 JSON: %w", name, err)
	}
	return m, nil
}

// SchemaNames 返回全部内嵌的 schema 文件名（有序）。
func SchemaNames() ([]string, error) {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// knownSchemaKeywords 是校验器认识的全部关键字。
var knownSchemaKeywords = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true,
	"type": true, "required": true, "properties": true,
	"additionalProperties": true, "items": true, "enum": true, "const": true,
	"minimum": true, "maximum": true, "minLength": true, "minItems": true,
	"minProperties": true, "$defs": true, "$ref": true,
}

// ValidateAgainstSchema 用 schema 校验一个已经序列化成 map 的对象。
func ValidateAgainstSchema(schema map[string]any, doc map[string]any) []string {
	v := &schemaValidator{defs: defsOf(schema)}
	return v.validate(schema, doc, "$")
}

type schemaValidator struct {
	defs map[string]any
}

func defsOf(schema map[string]any) map[string]any {
	out := map[string]any{}
	if raw, ok := schema["$defs"].(map[string]any); ok {
		for k, v := range raw {
			out[k] = v
		}
	}
	return out
}

// resolve 展开 $ref（只支持 #/$defs/<name> 这一种形式）。
func (v *schemaValidator) resolve(s map[string]any) map[string]any {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	if d, ok := v.defs[name]; ok {
		if m, ok := d.(map[string]any); ok {
			return m
		}
	}
	return s
}

func (v *schemaValidator) validate(schema map[string]any, doc any, path string) []string {
	schema = v.resolve(schema)
	var errs []string
	for k := range schema {
		if !knownSchemaKeywords[k] {
			errs = append(errs, fmt.Sprintf("%s: schema 含未支持的关键字 %q（校验器不静默放过未知关键字）", path, k))
		}
	}
	if t, ok := schema["type"].(string); ok {
		if !matchesType(t, doc) {
			return []string{fmt.Sprintf("%s: 类型应为 %s, 实得 %s", path, t, typeName(doc))}
		}
	}
	if c, ok := schema["const"]; ok {
		if !jsonEqual(c, doc) {
			errs = append(errs, fmt.Sprintf("%s: 应恒等于 %v, 实得 %v", path, c, doc))
		}
	}
	if raw, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range raw {
			if jsonEqual(e, doc) {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: 值 %v 不在枚举 %v 内", path, doc, raw))
		}
	}
	switch d := doc.(type) {
	case map[string]any:
		errs = append(errs, v.validateObject(schema, d, path)...)
	case string:
		if n, ok := numOf(schema["minLength"]); ok && float64(len(d)) < n {
			errs = append(errs, fmt.Sprintf("%s: 字符串长度 %d < minLength %v", path, len(d), n))
		}
	case []any:
		if n, ok := numOf(schema["minItems"]); ok && float64(len(d)) < n {
			errs = append(errs, fmt.Sprintf("%s: 数组长度 %d < minItems %v", path, len(d), n))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range d {
				errs = append(errs, v.validate(items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case float64:
		if n, ok := numOf(schema["minimum"]); ok && d < n {
			errs = append(errs, fmt.Sprintf("%s: %v < minimum %v", path, d, n))
		}
		if n, ok := numOf(schema["maximum"]); ok && d > n {
			errs = append(errs, fmt.Sprintf("%s: %v > maximum %v", path, d, n))
		}
	}
	return errs
}

func (v *schemaValidator) validateObject(schema map[string]any, doc map[string]any, path string) []string {
	var errs []string
	props, _ := schema["properties"].(map[string]any)
	if raw, ok := schema["required"].([]any); ok {
		for _, r := range raw {
			key, _ := r.(string)
			if _, present := doc[key]; !present {
				errs = append(errs, fmt.Sprintf("%s: 缺必填字段 %q", path, key))
			}
		}
	}
	if n, ok := numOf(schema["minProperties"]); ok && float64(len(doc)) < n {
		errs = append(errs, fmt.Sprintf("%s: 字段数 %d < minProperties %v", path, len(doc), n))
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := doc[k]
		if props != nil {
			if sub, ok := props[k].(map[string]any); ok {
				errs = append(errs, v.validate(sub, val, path+"."+k)...)
				continue
			}
		}
		switch ap := schema["additionalProperties"].(type) {
		case bool:
			if !ap {
				errs = append(errs, fmt.Sprintf("%s: 不允许的额外字段 %q", path, k))
			}
		case map[string]any:
			errs = append(errs, v.validate(ap, val, path+"."+k)...)
		}
	}
	return errs
}

func matchesType(t string, doc any) bool {
	switch t {
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	case "array":
		_, ok := doc.([]any)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "null":
		return doc == nil
	case "number":
		_, ok := doc.(float64)
		return ok
	case "integer":
		f, ok := doc.(float64)
		return ok && f == float64(int64(f))
	}
	return false
}

func typeName(doc any) string {
	switch d := doc.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if d == float64(int64(d)) {
			return "integer"
		}
		return "number"
	case nil:
		return "null"
	}
	return "unknown"
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}

func numOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// ValidateValue 把一个契约结构序列化后按 schema 校验。
func ValidateValue(name string, value any) []string {
	schema, err := LoadSchema(SchemaFile[name])
	if err != nil {
		return []string{fmt.Sprintf("读 schema %s 失败: %v", name, err)}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return []string{fmt.Sprintf("序列化 %s 失败: %v", name, err)}
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return []string{fmt.Sprintf("反序列化 %s 失败: %v", name, err)}
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s 序列化后不是 object", name)}
	}
	return ValidateAgainstSchema(schema, m)
}

// GoTypeFields 返回某个契约结构体的 JSON 字段名集合（按 json tag 解析）。
func GoTypeFields(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
