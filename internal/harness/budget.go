package harness

import (
	"fmt"
	"sync"
	"time"
)

// Budget 是 hermes 式预算护栏：限制一次 Run 的步数与截止时间。超额即停
// （fail-closed），保证 harness 不会因失控的策略死循环或外部调用挂住而无限运行。
// 它是"一系列机制"里的护栏机制，和 hermes 的 Budget(USD 上限) 同一思路——只是
// 这里收紧到步数/时间（本地闭环无需花钱）。
type Budget struct {
	MaxSteps int
	Deadline time.Time

	mu    sync.Mutex
	steps int
}

// NewBudget 构造预算护栏。maxSteps<=0 表示不限步；deadline<=0 表示不限期。
func NewBudget(maxSteps int, deadline time.Duration) *Budget {
	b := &Budget{MaxSteps: maxSteps}
	if deadline > 0 {
		b.Deadline = time.Now().Add(deadline)
	}
	return b
}

// Spend 消耗 n 步（线程安全）。
func (b *Budget) Spend(n int) {
	b.mu.Lock()
	b.steps += n
	b.mu.Unlock()
}

// Steps 已用步数。
func (b *Budget) Steps() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.steps
}

// Exhausted 预算是否耗尽（步数达上限或已过截止时间）。
func (b *Budget) Exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.MaxSteps > 0 && b.steps >= b.MaxSteps {
		return true
	}
	if !b.Deadline.IsZero() && time.Now().After(b.Deadline) {
		return true
	}
	return false
}

// RemainingSteps 剩余步数（负数表示已超）。
func (b *Budget) RemainingSteps() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.MaxSteps - b.steps
}

func (b *Budget) String() string {
	return fmt.Sprintf("budget steps=%d/%d", b.Steps(), b.MaxSteps)
}
