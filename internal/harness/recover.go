package harness

import (
	"fmt"
	"time"
)

// RecoverStep 把一步实验包进 panic + error 恢复：若 fn panic 或返回错误，标记该步
// 为已恢复（recovered）并继续，不丢整个 episode / 整个 run。返回是否发生了恢复。
// 这是 hermes 式「错误恢复」在本地闭环里的落点——单步失败（落盘错误、obs 渲染
// 异常、策略预测 panic）被就地隔离，闭环继续推进而不是整体崩溃。
func RecoverStep(rec *StepRecord, fn func() error) (recovered bool) {
	defer func() {
		if r := recover(); r != nil {
			rec.Terminated = true
			rec.Delta = 0
			rec.Recovered = true
			recovered = true
			_ = r // 吞掉 panic：记录已标记 recovered，由调用方决定后续
		}
	}()
	if err := fn(); err != nil {
		rec.Terminated = true
		rec.Delta = 0
		rec.Recovered = true
		recovered = true
	}
	return recovered
}

// Retry 以指数退避重试 fn，最多 attempts 次（attempts<=0 视为 1 次）。用于外部 /
// 易失败调用（如 bolloon CLI 子进程、OML 子进程）。对齐 hermes harness 的
// retry/backoff 纪律——随机网络/子进程失败不应让整个 harness 失败。
func Retry(attempts int, backoff time.Duration, fn func() error) error {
	if attempts <= 0 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		if err := fn(); err != nil {
			last = err
			// 指数退避：backoff * 2^i，最后一轮不再 sleep
			if i < attempts-1 {
				time.Sleep(backoff * time.Duration(1<<uint(i)))
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("retry: 全部 %d 次均失败: %w", attempts, last)
}

// ResumePoint 返回 store 中该 runID 已完成的最大 episode 下标 +1（即续跑起点）。
// 错误恢复时，Run 可跳到该点继续，不重跑已落盘的实验。无记录则返回 0。
func ResumePoint(store *Store, runID string) int {
	recs, err := store.Records()
	if err != nil || len(recs) == 0 {
		return 0
	}
	maxEp := -1
	for _, r := range recs {
		if r.RunID == runID && r.Episode > maxEp {
			maxEp = r.Episode
		}
	}
	if maxEp < 0 {
		return 0
	}
	return maxEp + 1
}
