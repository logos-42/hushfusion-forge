// json.go — 规范 JSON: 键序由调用方逐字给出, 浮点一律最短往返(契约 §2)。
//
// 这里刻意不走 encoding/json 的数字路径。json.Marshal 的浮点格式是另一套规则(它按
// 指数阈值在 'f' 与 'e' 之间切换), 而冻结契约写死的是
//
//	strconv.FormatFloat(v, 'g', -1, 64)
//
// 对大多数值两者"看起来一样", 但本协议的判据是逐字节相同, 不是"看起来一样"。
// 同理, 一张 map 的遍历顺序也是不确定的: 键序必须由调用方写死在代码里, 否则同一条
// 请求序列会产出不同的字节。
package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// raw 是一段**已经是**规范 JSON 的文本。
type raw string

// str 只把字符串交给 encoding/json 做转义 —— 字符串没有"最短往返"的问题, 浮点才有。
func str(s string) raw {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal 对 string 只会在无效 UTF-8 上失败; 那时宁可大声失败, 也不要
		// 悄悄写出一行不是 JSON 的响应。
		panic(fmt.Sprintf("world: cannot encode string %q: %v", s, err))
	}
	return raw(b)
}

// flt 把 float64 写成契约 §2 要求的形式。
//
// 非有限值直接 panic, 而不是写成 0 / null: 把一个 NaN 变成 0.0 正是本仓最反对的
// "编造数字"。调用方(cmd/forge)会把 panic 转成一个 internal 错误响应并非零退出。
func flt(v float64) raw {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		panic(fmt.Sprintf("world: refusing to serialise a non-finite float (%v) — "+
			"a fabricated 0.0 here would be invisible downstream", v))
	}
	return raw(strconv.FormatFloat(v, 'g', -1, 64))
}

func itg(v int) raw { return raw(strconv.Itoa(v)) }

func boolean(v bool) raw {
	if v {
		return raw("true")
	}
	return raw("false")
}

// kv 是一个键值对; 顺序就是它在响应里出现的顺序。
type kv struct {
	k string
	v raw
}

// obj 按给定次序渲染一个 JSON 对象。
func obj(pairs ...kv) raw {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(str(p.k)))
		b.WriteByte(':')
		b.WriteString(string(p.v))
	}
	b.WriteByte('}')
	return raw(b.String())
}

// arr 渲染一个 JSON 数组。
func arr(items []raw) raw {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(it))
	}
	b.WriteByte(']')
	return raw(b.String())
}

// rawArr 渲染一个浮点数组, 每个元素都按最短往返写。
func rawArr(vs []float64) raw {
	items := make([]raw, 0, len(vs))
	for _, v := range vs {
		items = append(items, flt(v))
	}
	return arr(items)
}

// strArr 渲染一个字符串数组。
func strArr(vs []string) raw {
	items := make([]raw, 0, len(vs))
	for _, v := range vs {
		items = append(items, str(v))
	}
	return arr(items)
}

// intArr 渲染一个整数数组(info.clamped 用它报"哪些参数撞到了盒子/钳位")。
func intArr(vs []int) raw {
	items := make([]raw, 0, len(vs))
	for _, v := range vs {
		items = append(items, itg(v))
	}
	return arr(items)
}

// SpecCanonicalJSON 是 spec 的**规范 JSON**: 键排序 + 最短往返浮点(契约 §3.1)。
//
// sha256 字段不参与它自己的哈希(那是循环的), 因此规范 JSON 里没有这个键 —— 被哈希的
// 对象恰好是 {lower, n_coils, n_params, upper} 这四个键。
func SpecCanonicalJSON(spec config.Spec) string {
	// 键序 lower < n_coils < n_params < upper 是字节序, 不是"我抄的顺序"。
	return string(obj(
		kv{"lower", rawArr(spec.Lower())},
		kv{"n_coils", itg(spec.NCoils)},
		kv{"n_params", itg(spec.NParams())},
		kv{"upper", rawArr(spec.Upper())},
	))
}

// SpecSHA256 是规范 JSON 的 sha256(小写十六进制)。
//
// 客户端(另一种语言)复算它的价值在于: 它证明 spec 的数字跨过进程边界之后仍是同一批
// 数字(规范 JSON 让"最短往返"这条规则可被独立重实现), 而不是证明"我们相信 Go"。
func SpecSHA256(spec config.Spec) string {
	sum := sha256.Sum256([]byte(SpecCanonicalJSON(spec)))
	return hex.EncodeToString(sum[:])
}

// specJSON 是 hello 里那个 spec 对象: 键序照契约 §3.1 的示例, 末尾附上 sha256。
func specJSON(spec config.Spec) raw {
	return obj(
		kv{"n_coils", itg(spec.NCoils)},
		kv{"n_params", itg(spec.NParams())},
		kv{"lower", rawArr(spec.Lower())},
		kv{"upper", rawArr(spec.Upper())},
		kv{"sha256", str(SpecSHA256(spec))},
	)
}
