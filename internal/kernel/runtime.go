package kernel

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// model-runtime 移植 (K6): acquire 只读租约 —— 连接复用 / 超时 / 回退 / 熔断 / 退避。
// 只读: 绝不改 provider 配置; 真正的 provider 调用由 openConnection 端口注入。
//
// 未随迁 (诚实边界, 现有调用面不涉及): maxConcurrency 并发等待队列 + 槽转让。

// 退避策略 (写成数据; 实现与测试共用)。
const (
	backoffBaseMs  = 200
	backoffFactor  = 2
	backoffMaxMs   = 5000
	backoffJitter  = 0.25
	backoffRetries = 3
)

// 熔断策略。
const (
	breakerFailureThreshold = 3
	breakerCooldownMs       = 30_000
	breakerHalfOpenProbes   = 1
)

const defaultTimeoutMs = 30_000

// modelPoolKey: 只用只读字段 (provider/model/baseUrl), 与调用内容无关。
func modelPoolKey(s map[string]any) string {
	return fmt.Sprintf("%s::%s::%s", asString(s["provider"]), asString(s["model"]), asString(s["baseUrl"]))
}

// isRateLimited: 端口可把限流表达为 status=429 或 error 文本含 429/rate limit。
func isRateLimited(x any) bool {
	o, ok := x.(map[string]any)
	if !ok {
		return false
	}
	switch v := o["status"].(type) {
	case int:
		if v == 429 {
			return true
		}
	case string:
		if v == "429" {
			return true
		}
	}
	if s, ok := o["error"].(string); ok {
		if containsAny(s, "429", "rate limit", "rate-limit", "ratelimit") {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// backoffDelayMs: min(base*factor^n, max) 再乘 ±jitter; retryAfterMs 存在时取最大值。
func backoffDelayMs(n int, retryAfterMs int64, rnd func() float64) int64 {
	exp := int64(backoffBaseMs)
	for i := 0; i < n && exp < backoffMaxMs; i++ {
		exp *= backoffFactor
	}
	if exp > backoffMaxMs {
		exp = backoffMaxMs
	}
	jittered := float64(exp) * (1 + backoffJitter*(2*rnd()-1))
	out := int64(jittered)
	if out < 0 {
		out = 0
	}
	if retryAfterMs > out {
		out = retryAfterMs
	}
	return out
}

// breaker 状态。
const (
	breakerClosed = iota
	breakerOpen
	breakerHalfOpen
)

type breaker struct {
	failures  int
	state     int
	openUntil int64
	probes    int
}

type connSlot struct {
	conn   Conn
	leases int
}

// ModelRuntime 是模型只读租约运行时。
type ModelRuntime struct {
	ports            *Ports
	defaultTimeoutMs int64
	maxConcurrency   int
	now              func() int64
	rnd              func() float64

	mu       sync.Mutex
	pool     map[string]*connSlot
	breakers map[string]*breaker

	stats modelStats
}

type modelStats struct {
	reused, opened, timeouts, aborts int64
	rateLimited, retries             int64
	circuitOpened, failFast          int64
}

// NewModelRuntime 构造运行时; 缺 openConnection 端口 ⇒ 报错 (构造期硬约束)。
func NewModelRuntime(p *Ports) (*ModelRuntime, error) {
	if p == nil || p.OpenConnection == nil {
		return nil, errors.New("ModelRuntime 需要 openConnection 端口")
	}
	return &ModelRuntime{
		ports:            p,
		defaultTimeoutMs: defaultTimeoutMs,
		maxConcurrency:   4,
		now:              func() int64 { return time.Now().UnixMilli() },
		rnd:              rand.Float64,
		pool:             map[string]*connSlot{},
		breakers:         map[string]*breaker{},
	}, nil
}

// Snapshot 返回只读诊断统计。
func (rt *ModelRuntime) Snapshot() map[string]any {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return map[string]any{
		"connections": len(rt.pool), "defaultTimeoutMs": rt.defaultTimeoutMs,
		"reused": rt.stats.reused, "opened": rt.stats.opened, "timeouts": rt.stats.timeouts,
		"aborts": rt.stats.aborts, "rateLimited": rt.stats.rateLimited, "retries": rt.stats.retries,
		"circuitOpened": rt.stats.circuitOpened, "failFast": rt.stats.failFast,
	}
}

// Acquire 取一个只读租约 (可能需要先开连接; 同一 pool key 复用)。
func (rt *ModelRuntime) Acquire(snapshot map[string]any, opts map[string]any) (map[string]any, error) {
	if asString(snapshot["provider"]) == "" || asString(snapshot["model"]) == "" {
		return nil, errors.New("acquire 需要 provider + model (缺了不许猜)")
	}
	// ① 能力检查 (拒在开连接之前)。
	if opts != nil {
		if need, ok := opts["require"].([]any); ok && len(need) > 0 {
			caps, has := snapshot["capabilities"].([]any)
			if !has {
				return nil, fmt.Errorf("未知能力 (snapshot 未声明 capabilities, 无法确认 %v)", need)
			}
			have := map[string]bool{}
			for _, c := range caps {
				have[asString(c)] = true
			}
			var missing []string
			for _, c := range need {
				if !have[asString(c)] {
					missing = append(missing, asString(c))
				}
			}
			if len(missing) > 0 {
				return nil, fmt.Errorf("缺能力: %v", missing)
			}
		}
	}
	slot, err := rt.ensureConnection(snapshot)
	if err != nil {
		return nil, err
	}
	key := modelPoolKey(snapshot)
	released := false
	rt.mu.Lock()
	slot.leases++
	rt.mu.Unlock()
	lease := map[string]any{
		"snapshot": snapshot,
		"release": func() {
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if released {
				return
			}
			released = true
			if cur, ok := rt.pool[key]; ok && cur.leases > 0 {
				cur.leases--
			}
		},
		"call": func(req map[string]any) map[string]any {
			return rt.call(snapshot, req)
		},
	}
	return lease, nil
}

// ensureConnection 连接池: 同一 pool key 复用; 并发首开只保留一个。
func (rt *ModelRuntime) ensureConnection(snapshot map[string]any) (*connSlot, error) {
	key := modelPoolKey(snapshot)
	rt.mu.Lock()
	if slot, ok := rt.pool[key]; ok {
		rt.stats.reused++
		rt.mu.Unlock()
		return slot, nil
	}
	rt.mu.Unlock()

	conn, err := rt.ports.OpenConnection(snapshot)
	if err != nil {
		return nil, err
	}
	slot := &connSlot{conn: conn}
	rt.mu.Lock()
	if existing, ok := rt.pool[key]; ok {
		rt.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	rt.pool[key] = slot
	rt.stats.opened++
	rt.mu.Unlock()
	return slot, nil
}

// call 走候选清单 (主 provider + fallbacks) 的一次调用, 带熔断 / 退避 / 超时。
func (rt *ModelRuntime) call(snapshot map[string]any, req map[string]any) map[string]any {
	budget := rt.defaultTimeoutMs
	if v, ok := toInt64(snapshot["timeoutMs"]); ok {
		budget = v
	}
	candidates := []map[string]any{snapshot}
	if fps, ok := snapshot["fallbackProviders"].([]any); ok {
		for _, fp := range fps {
			if pm, ok := fp.(map[string]any); ok {
				c := shallowCopy(snapshot)
				c["provider"] = pm["provider"]
				candidates = append(candidates, c)
			} else if ps := asString(fp); ps != "" && ps != asString(snapshot["provider"]) {
				c := shallowCopy(snapshot)
				c["provider"] = ps
				candidates = append(candidates, c)
			}
		}
	}
	started := rt.now()
	last := map[string]any{"ok": false, "error": "没有可用的 provider"}
	for ci, snap := range candidates {
		key := modelPoolKey(snap)
		isFallback := ci > 0
		if until, open := rt.breakerGate(key); open {
			rt.mu.Lock()
			rt.stats.failFast++
			rt.mu.Unlock()
			if ci+1 < len(candidates) {
				last = map[string]any{"ok": false, "provider": asString(snap["provider"]), "fallback": isFallback, "error": fmt.Sprintf("熔断中 (冷却至 %d)", until)}
				continue
			}
			last = map[string]any{"ok": false, "error": fmt.Sprintf("熔断中 (冷却至 %d)", until)}
			last["ms"] = rt.now() - started
			return last
		}
		slot, err := rt.ensureConnection(snap)
		if err != nil {
			last = map[string]any{"ok": false, "error": err.Error(), "provider": asString(snap["provider"]), "fallback": isFallback}
			continue
		}
		for attempt := 0; ; attempt++ {
			res, cerr := rt.callOnce(slot.conn, req, budget)
			if cerr != nil {
				var te *modelTimeoutError
				if errors.As(cerr, &te) {
					if ci+1 < len(candidates) {
						last = map[string]any{"ok": false, "error": cerr.Error(), "provider": asString(snap["provider"]), "fallback": isFallback}
						break
					}
					last = map[string]any{"ok": false, "error": cerr.Error()}
					last["ms"] = rt.now() - started
					return last
				}
				rt.onFailure(key)
				last = map[string]any{"ok": false, "error": cerr.Error(), "provider": asString(snap["provider"]), "fallback": isFallback}
				break
			}
			if ok, _ := res["ok"].(bool); ok {
				rt.onSuccess(key)
				out := shallowCopy(res)
				out["provider"] = asString(snap["provider"])
				out["fallback"] = isFallback
				out["ms"] = rt.now() - started
				return out
			}
			if isRateLimited(res) {
				rt.mu.Lock()
				rt.stats.rateLimited++
				rt.mu.Unlock()
				if attempt >= backoffRetries {
					last = shallowCopy(res)
					last["provider"] = asString(snap["provider"])
					last["error"] = fmt.Sprintf("限流重试用尽 (%d 次)", attempt+1)
					break
				}
				wait := backoffDelayMs(attempt, 0, rt.rnd)
				rt.mu.Lock()
				rt.stats.retries++
				rt.mu.Unlock()
				time.Sleep(time.Duration(wait) * time.Millisecond)
				continue
			}
			rt.onFailure(key)
			last = shallowCopy(res)
			last["provider"] = asString(snap["provider"])
			last["fallback"] = isFallback
			last["ms"] = rt.now() - started
			break
		}
	}
	last["ms"] = rt.now() - started
	return last
}

type modelTimeoutError struct{ budgetMs int64 }

func (e *modelTimeoutError) Error() string {
	return fmt.Sprintf("模型调用超时 (%dms)", e.budgetMs)
}

func (rt *ModelRuntime) callOnce(conn Conn, req map[string]any, budget int64) (map[string]any, error) {
	type result struct {
		m   map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		m, err := conn.Call(req)
		ch <- result{m, err}
	}()
	select {
	case r := <-ch:
		return r.m, r.err
	case <-time.After(time.Duration(budget) * time.Millisecond):
		rt.mu.Lock()
		rt.stats.timeouts++
		rt.mu.Unlock()
		return nil, &modelTimeoutError{budgetMs: budget}
	}
}

func (rt *ModelRuntime) breakerGate(key string) (int64, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b, ok := rt.breakers[key]
	if !ok || b.state == breakerClosed {
		return 0, false
	}
	now := rt.now()
	if b.state == breakerOpen && now >= b.openUntil {
		b.state = breakerHalfOpen
		b.probes = 0
	}
	if b.state == breakerOpen {
		return b.openUntil, true
	}
	if b.probes >= breakerHalfOpenProbes {
		return b.openUntil, true
	}
	b.probes++
	return 0, false
}

func (rt *ModelRuntime) onSuccess(key string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b, ok := rt.breakers[key]
	if !ok {
		return
	}
	if b.state == breakerHalfOpen {
		b.state = breakerClosed
		b.failures = 0
		b.probes = 0
		return
	}
	b.failures = 0
}

func (rt *ModelRuntime) onFailure(key string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b, ok := rt.breakers[key]
	if !ok {
		b = &breaker{state: breakerClosed}
		rt.breakers[key] = b
	}
	b.failures++
	halfOpenFail := b.state == breakerHalfOpen
	if halfOpenFail || b.failures >= breakerFailureThreshold {
		b.state = breakerOpen
		b.openUntil = rt.now() + breakerCooldownMs
		b.probes = 0
		rt.stats.circuitOpened++
	}
}

// Close 关闭连接池 (进程收尾用)。
func (rt *ModelRuntime) Close() error {
	rt.mu.Lock()
	slots := make([]*connSlot, 0, len(rt.pool))
	for _, s := range rt.pool {
		slots = append(slots, s)
	}
	rt.pool = map[string]*connSlot{}
	rt.mu.Unlock()
	for _, s := range slots {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	}
	return nil
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}
