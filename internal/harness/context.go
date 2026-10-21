package harness

import (
	"math/rand"
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/mu"
)

// RunContext 是 harness 一次运行的「专属上下文」：把原先散落在 Harness 上的运行态
// （rng / 动作空间 / 区域 / 目标分 / 预算 / store 句柄）收拢成显式、隔离、可派生子
// 上下文的对象。每个 Run 都从自己的 RunContext 起，互不串扰——对齐 hermes harness
// 的上下文隔离：每次调用都带专属 ctx，而非共享全局可变态。
//
// 这样 harness 可被并发跑多个独立 Run（各自 context），也便于从中断处 resume
// （context 自带 rng 子源与预算护栏）。
type RunContext struct {
	Space   mu.ActionSpace
	Regions []mu.Region
	Target  float64
	Store   *Store
	Budget  *Budget

	mu  sync.Mutex
	rng *rand.Rand
}

// NewRunContext 构造一次运行的专属上下文。
func NewRunContext(space mu.ActionSpace, regions []mu.Region, target float64, store *Store, seed int64, b *Budget) *RunContext {
	return &RunContext{
		Space:   space,
		Regions: regions,
		Target:  target,
		Store:   store,
		Budget:  b,
		rng:     rand.New(rand.NewSource(seed)),
	}
}

// ForkEpisode 派生一条 episode 子上下文（专属、隔离的 rng 子源，避免跨 episode 串扰）。
// 子上下文继承父 context 的空间/区域/预算，但拥有独立随机源，保证可复现且不串扰。
func (rc *RunContext) ForkEpisode(ep, seed int) *EpisodeContext {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	sub := rand.New(rand.NewSource(int64(seed)*1000003 + int64(ep)))
	return &EpisodeContext{Run: rc, Episode: ep, Seed: seed, rng: sub}
}

// EpisodeContext 是单条 episode 的专属子上下文（一次实验候选 = 一条「模型设计」）。
type EpisodeContext struct {
	Run     *RunContext
	Episode int
	Seed    int
	rng     *rand.Rand
}

// RNG 返回该子上下文的专属随机源。
func (ec *EpisodeContext) RNG() *rand.Rand { return ec.rng }

// Space / Regions / Target / Budget 透出父 context 的只读配置。
func (ec *EpisodeContext) Space() mu.ActionSpace { return ec.Run.Space }
func (ec *EpisodeContext) Regions() []mu.Region  { return ec.Run.Regions }
func (ec *EpisodeContext) Target() float64       { return ec.Run.Target }
func (ec *EpisodeContext) Budget() *Budget       { return ec.Run.Budget }
