// Package owners 是并行构建所用的、可被机器校验的文件归属名册。
//
// 按委派协议：只有当名册 (a) 互不重叠且 (b) 覆盖完整，并且这一事实是由
// 测试而不是由人对着两个列表核对出来的时候，并行线才是安全的。
// owners_test.go 就是那道检查；它还会改动名册，以证明这道检查确实
// 能变红。
package owners

import (
	"path/filepath"
	"strings"
)

// Stage 把一组路径分配给唯一的负责人。
//
// 路径可以是精确文件，也可以是目录前缀（结尾带 "/"）—— 父线保留根目录、
// config 和 testdata；每条并行线拥有整个包，因此即使它们新增文件，
// 两条线也不会碰撞。
type Stage struct {
	ID    string
	Name  string
	Owner string
	Paths []string
}

// Stages 是冻结的名册。新增一个阶段或移动一条路径属于契约变更：
// 必须在同一个 commit 里同步反映到 CONTRACT.md。
var Stages = []Stage{
	{
		ID:    "root",
		Name:  "frozen shared skeleton (go.mod, config, roster, contracts, golden data, acceptance gates)",
		Owner: "parent",
		Paths: []string{
			"go.mod",
			"go.sum", // 2026-10-21 新增：bubbletea v2 等外部依赖引入 go.sum，随 go.mod 归 root
			"LICENSE",
			"README.md",
			"README.en.md",
			"PLAN.md",
			"CONTRACT.md",
			"CHANGELOG.md",
			".gitignore",
			"internal/app/",
			"internal/config/",
			"internal/owners/",
			"internal/kernel/", // 2026-10-23 原生化：Bolloon kernel 机制原语的 Go 移植（原 internal/kernelbridge + Node bridge/ 已删），归 parent
			"internal/ai/",
			"internal/bolloon/",  // 2026-10-20 新增：BOLLOON CLI 强类型客户端 + forge bolloon 透传（归 parent）
			"internal/osview/",   // 2026-10-20 新增：OS 决策层只读视图层（供 forge os 与 TUI 共用，归 parent）
			"internal/oskernel/", // 2026-10-20 新增：OS 宏内核（syscall 分发 + 子系统驱动 + 写前守卫，归 parent）
			"internal/aios/",     // 2026-10-20 新增：phase-3 AI 粘合层（AI 经内核受控 syscall 提议，归 parent）
			"internal/modules/",
			"internal/mcp/",       // 2026-10-21 新增：MCP stdio 客户端 + init 门 + 权限门（学 Crush，归 parent）
			"internal/lsp/",       // 2026-10-21 新增：LSP server 客户端 + manager + init 门 + 权限门（学 Hermes/Crush，归 parent）
			"internal/agent/",     // 2026-10-23 新增：Agent Runtime（单一 queryLoop + 会话记忆 + skills + 目标/计划/待办 + serve + 上下文压缩，见 docs/wiki/agent-runtime-design.md，归 parent）
			"internal/identity/",  // 2026-10-23 新增：DID 身份（did:key/ed25519）+ 能力令牌，零依赖（归 parent）
			"internal/wallet/",    // 2026-10-23 新增：EVM 钱包（secp256k1 + did:pkh + EIP-712 签名），归 parent
			"internal/x402/",      // 2026-10-23 新增：x402 客户端（402 解析 + EIP-3009 TransferWithAuthorization），归 parent
			"internal/cron/",      // 2026-10-24 新增：内部 cron（5 段表达式解析 + Next/Due + JSON 持久化），归 parent
			"internal/dchat/",     // 2026-10-24 新增：去中心化通信频道（iroh/hyperswarm/orbitdb，Go↔Node JSON 行协议），归 parent
			"internal/envload/",   // 2026-10-24 新增：.env 加载与保护（不覆盖已有 env / 密钥脱敏 / 防路径穿越），归 parent
			"internal/subagents/", // 2026-10-24 新增：子智能体集群（并发上限 / 可查询 / 可停止 / 深度由宿主控制），归 parent
			"internal/workspace/", // 2026-10-24 新增：workspace 分类索引（project/design/acceptance/evidence/receipts/tools…），归 parent
			"internal/webapp/",    // 2026-10-24 新增：桌面/Web 外壳（本地 HTTP + 内嵌前端 + 自动更新），归 parent
			"dist/",               // 2026-10-24 新增：发布打包产物（gitignore，但 roster 需覆盖以免门误报），归 parent
			"npm/",                // 2026-10-24 新增：npm 启动器 + Node SDK（0.2.3），归 parent
			"internal/sandbox/",   // 2026-10-23 新增：exec 围栏（目录 jail + env 白名单 + 进程组 kill + 超时，归 parent）
			"internal/proc/",      // 2026-10-23 新增：进程管理（登记表 + 取消传播 + 进程组停止 + 回收 + 并发上限，归 parent）
			"internal/persona/",   // 2026-10-23 新增：persona/agent 文档发现 + bootstrap 注入（归 parent）
			"internal/review/",    // 2026-10-23 新增：目标/计划/结果二审（独立 reviewer + 人工门，fail-closed，归 parent）
			"testdata/",
			"scripts/",
			"knowledge/",
			"artifacts/",
		},
	},
	{
		ID:    "wiki",
		Name:  "wiki-first knowledge system (docs/wiki, manifests, platform config, wiki-lint CI)",
		Owner: "parent",
		Paths: []string{
			"docs/",
			"manifests/",
			// os/: HUSHFUSION OS 决策层（任务池/实验状态机/机会池/决策日志/周复盘）。
			// 它是 wiki-first 的延伸：Markdown + JSON + 脚本 + Git，不承载引擎逻辑，
			// 证据的唯一权威仍在 runs/ 与 docs/wiki/。新增 os/ 文件属于合同变更。
			"os/",
			"AGENTS.md",
			"CLAUDE.md",
			".cursorrules",
			".windsurfrules",
			".claude/",
			".github/",
		},
	},
	{
		ID:    "A",
		Name:  "physics: magnetostatics, metrics, geometry (numerical core)",
		Owner: "agent-A",
		Paths: []string{"internal/physics/"},
	},
	{
		ID:    "B",
		Name:  "objective + human baselines",
		Owner: "agent-B",
		Paths: []string{"internal/objective/", "internal/baseline/"},
	},
	{
		ID:    "C",
		Name:  "design registry + experiment runner",
		Owner: "agent-C",
		Paths: []string{"internal/registry/", "internal/runner/"},
	},
	{
		ID:    "D",
		Name:  "search algorithms (random, lhs, evolution, evolution_warm)",
		Owner: "agent-D",
		Paths: []string{"internal/search/"},
	},
	{
		ID:    "E",
		Name:  "benchmark harness + knowledge mining + report rendering",
		Owner: "agent-E",
		Paths: []string{"internal/experiment/", "internal/knowledge/", "internal/report/"},
	},
	{
		ID:    "F",
		Name:  "RL environment + CLI wiring (wiring is a single-owner round, done last)",
		Owner: "agent-F",
		// internal/world/ 是同一轮的所有权: 世界协议(docs/world-protocol.md)只是把
		// rlenv 的语义搬到进程边界之外, 它和 rlenv、CLI 接线是同一段工作。
		// 新增路径 = 合同变更, 已在 CONTRACT.md §2 同步(2026-09-27)。
		// internal/mu/ 同属这一轮: 交换子世界的算子与控制代数(上游 GCA/TD 的 Go 移植),
		// 它是 --world mu 的语义底座, 与 internal/world/ 是同一段工作。
		// internal/harness/（2026-10-20 新增）：本地自优化闭环，驱动 mu 世界跑实验、
		// 落盘、自我调整，并接 cmd/ 的 forge harness 子命令 —— 与 mu/rlenv/CLI 同属一轮。
		Paths: []string{"internal/rlenv/", "cmd/", "internal/world/", "internal/mu/", "internal/harness/"},
	},
	{
		ID:    "G",
		Name:  "Python auxiliary layer: reference/oracle, analysis, schema parity",
		Owner: "agent-G",
		Paths: []string{"python/"},
	},
	{
		ID:    "design",
		Name:  "internal design gate layer: ProjectionPhysics closed forms + six gates + upstream anchors",
		Owner: "design",
		Paths: []string{"internal/design/"},
	},
	{
		// 2026-10-21 新增: 控制织物（Control Fabric）第一片 —— 契约层 + 安全内核 +
		// 仿真装置 + 控制闭环。四者是一轮: 契约层定义交换格式, 安全内核判决,
		// 仿真装置提供被控对象, 闭环把它们串起来。拆开会得到"有格式没对象"或
		// "有对象没边界"的半成品, 所以同属一个 stage。
		ID:    "fabric",
		Name:  "control fabric: contracts + safety kernel + simulated plant + control loop",
		Owner: "parent",
		Paths: []string{
			"internal/contracts/",
			"internal/safety/",
			"internal/plant/",

			"internal/control/",
			"internal/contracts_bridge/",
			"internal/plant_eval/",
		},
	},
}

// Overlaps 返回冲突的配对：完全相同的路径、重复的前缀，或一条线的前缀
// 包含另一条线的。返回空表示名册有效。
func Overlaps(stages []Stage) [][2]string {
	var out [][2]string
	seen := map[string]string{}
	for _, s := range stages {
		for _, p := range s.Paths {
			if owner, dup := seen[p]; dup {
				out = append(out, [2]string{owner, s.ID + ":" + p})
				continue
			}
			seen[p] = s.ID
		}
	}
	for _, a := range stages {
		for _, b := range stages {
			if a.ID >= b.ID {
				continue
			}
			for _, pa := range a.Paths {
				for _, pb := range b.Paths {
					if !strings.HasSuffix(pa, "/") || !strings.HasSuffix(pb, "/") {
						continue
					}
					if strings.HasPrefix(pb, pa) || strings.HasPrefix(pa, pb) {
						out = append(out, [2]string{a.ID + ":" + pa, b.ID + ":" + pb})
					}
				}
			}
		}
	}
	return out
}

// Owner 返回拥有某个仓库相对路径的阶段，无人认领时返回 ""。
// 精确文件匹配优先于目录前缀。
func Owner(rel string, stages []Stage) string {
	best := ""
	bestLen := -1
	for _, s := range stages {
		for _, p := range s.Paths {
			if !strings.HasSuffix(p, "/") {
				if p == rel {
					return s.ID
				}
				continue
			}
			if strings.HasPrefix(rel, p) && len(p) > bestLen {
				best = s.ID
				bestLen = len(p)
			}
		}
	}
	return best
}

// Covered 返回被某个阶段认领的那些文件的子集。
func Covered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) != "" {
			out = append(out, f)
		}
	}
	return out
}

// Uncovered 返回没有任何阶段认领的文件。
func Uncovered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) == "" {
			out = append(out, f)
		}
	}
	return out
}

// IsTracked 报告某个仓库相对路径是否应当参与覆盖检查
// （生成产物以及 VCS / 缓存会被忽略）。
func IsTracked(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.HasPrefix(rel, ".git/") {
		return false
	}
	// .kilo/ .kilocode/: 别的 agent 工具在仓里留下的 worktree 检出(未跟踪, 不受本仓名册
	// 管辖)。名册门的名字说的是"每个被跟踪文件", 判别人的检出只会让它长期假红 ——
	// 假红比没有门更伤。新增跳过项时必须同时加一条"真文件仍然可见"的断言。
	// .hermes/: Hermes agent 的临时产物目录(提交信息草稿等), 同样不归名册管辖。
	// .crush/: Crush 编辑器状态目录(sqlite 等), 同上——别的工具的工作记忆, 不是本仓资产。
	for _, frag := range []string{
		"runs/", "__pycache__/", ".pytest_cache/", ".venv/", "node_modules/", ".DS_Store",
		".egg-info/", "go-build/", ".kilo/", ".kilocode/", ".hermes/", ".crush/",
	} {
		if strings.Contains(rel, frag) {
			return false
		}
	}
	if strings.HasSuffix(rel, ".pyc") || strings.HasSuffix(rel, ".log") {
		return false
	}
	return true
}
