// Package bolloon: 把本地已装的 BOLLOON 去中心化智能体 CLI 包成强类型的 Go 客户端。
//
// BOLLOON（@bolloon/bolloon-agent，本机 `bolloon` 二进制）是一个去中心化多智能体
// 框架：network 入网、agent 注册/发现、task 发布/认领/收发、engine 委派任务给外部
// 编码智能体。它是你之前落地的"去中心化案例"——核聚变系统通过这里与外部服务器上的
// 多个智能体交流（需求 2），也正是"调用 BOLLOON 的 cli"（需求 3）。
//
// 设计纪律：
//   - 所有调用都带 `--json`，解析 BOLLOON 的 §2 冻结信封 {ok, code, message, data}；
//     ok:false 一律 fail-closed（绝不假装成功）。
//   - 二进制缺失 / 找不到时 Detect 直接报错，不静默降级。
//   - 这是 CLI 的强类型壳；更"内核"的执行路径走 internal/kernelbridge 的
//     BolloonExec（把同一次 CLI 调用放进 @bolloon/bolloon-kernel 的 ChannelActor
//     里串行/审计执行），两者共用同一个 bolloon 二进制。
package bolloon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// EnvBin 允许用环境变量指到 bolloon 二进制（测试与非常规安装场景）。
const EnvBin = "BOLLOON_BIN"

// Envelope 是 bolloon --json 输出的 §2 冻结信封。
type Envelope struct {
	OK          bool            `json:"ok"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Data        json.RawMessage `json:"data"`
	NextAction  string          `json:"next_action"`
}

// Error 是 ok:false 时返回的失败（携带 code 与 next_action，便于调用方决策）。
type Error struct {
	Code       string
	Message    string
	NextAction string
}

func (e *Error) Error() string {
	if e.NextAction != "" {
		return fmt.Sprintf("bolloon: code=%s: %s (next: %s)", e.Code, e.Message, e.NextAction)
	}
	return fmt.Sprintf("bolloon: code=%s: %s", e.Code, e.Message)
}

// Client 是一个 bolloon CLI 会话。
type Client struct {
	bin     string
	timeout time.Duration
}

// Detect 找到 bolloon 二进制：优先级 BOLLOON_BIN 环境变量 → PATH 里的 bolloon →
// 本机全局安装路径。都找不到则报错（不静默降级）。
func Detect() (string, error) {
	if v := os.Getenv(EnvBin); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("%s=%q 不存在: %w", EnvBin, v, err)
		}
		return v, nil
	}
	if p, err := exec.LookPath("bolloon"); err == nil {
		return p, nil
	}
	candidate := "/Users/apple/.npm-global/bin/bolloon"
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("未找到 bolloon 二进制: 请 `npm i -g @bolloon/bolloon-agent` 或设置 %s", EnvBin)
}

// New 用 Detect 解析二进制后构造 Client。
func New() (*Client, error) {
	bin, err := Detect()
	if err != nil {
		return nil, err
	}
	return &Client{bin: bin, timeout: 90 * time.Second}, nil
}

// NewWithBin 用显式二进制路径构造（测试与非常规安装用）。
func NewWithBin(bin string) *Client {
	return &Client{bin: bin, timeout: 90 * time.Second}
}

// Bin 返回采用的二进制路径。
func (c *Client) Bin() string { return c.bin }

// Run 执行 `bolloon <args> --json` 并解析信封。ok:false 或非 JSON 输出都返回 *Error。
func (c *Client) Run(ctx context.Context, args ...string) (*Envelope, error) {
	if !hasArg(args, "--json") {
		args = append(args, "--json")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	env := &Envelope{}
	raw := bytesTrimSpace(stdout.Bytes())
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, env); err != nil || env.Code == "" {
			// 非信封输出（极少数无 --json 支持的子命令）：按失败处理，原样保留。
			env = &Envelope{
				OK:      false,
				Code:    "PARSE",
				Message: string(raw),
			}
		}
	} else {
		env = &Envelope{
			OK:      false,
			Code:    "NO_OUTPUT",
			Message: stderr.String(),
		}
	}

	if !env.OK {
		// 即便子进程非零退出，只要信封明确 ok:false 就以信封为准。
		if runErr != nil && env.Code == "PARSE" && env.Message == "" {
			env.Message = runErr.Error()
		}
		return env, &Error{Code: env.Code, Message: env.Message, NextAction: env.NextAction}
	}
	return env, nil
}

// ── 强类型方法（信封 data 已验证 schema 的） ───────────────────────────────

// NetworkStatusData 是 `bolloon network status --json` 的 data 形状。
type NetworkStatusData struct {
	DID             string        `json:"did"`
	Name            string        `json:"name"`
	JoinedNetworks  []any         `json:"joinedNetworks"`
	GatewayJoin     *GatewayJoin  `json:"gatewayJoin"`
}

// GatewayJoin 是入网网关信息（含 P2P peerId 与可拨入的 networkLink）。
type GatewayJoin struct {
	URL         string `json:"url"`
	DID         string `json:"did"`
	PeerID      string `json:"peerId"`
	NetworkLink string `json:"networkLink"`
	NetworkID   string `json:"networkId"`
}

// NetworkStatus 读取本机入网态（DID / 已加入网络 / 网关 peerId）。
func (c *Client) NetworkStatus(ctx context.Context) (*NetworkStatusData, error) {
	env, err := c.Run(ctx, "network", "status")
	if err != nil {
		return nil, err
	}
	var d NetworkStatusData
	if len(env.Data) > 0 {
		if e := json.Unmarshal(env.Data, &d); e != nil {
			return nil, fmt.Errorf("bolloon network status: 解码 data: %w", e)
		}
	}
	return &d, nil
}

// TaskBoardData 是 `bolloon task board --json` 的 data 形状。
type TaskBoardData struct {
	Count    int          `json:"count"`
	Local    int          `json:"local"`
	Remote   int          `json:"remote"`
	RegistryReady bool    `json:"registryReady"`
	Entries  []TaskEntry  `json:"entries"`
}

// TaskEntry 是任务板上的一条公告（外部智能体的协作请求）。
type TaskEntry struct {
	AnnouncementID string `json:"announcementId"`
	Capability     string `json:"capability"`
	Buyer          string `json:"buyer"`
	Status         string `json:"status"`
	Budget         string `json:"budget"`
	Deadline       string `json:"deadline"`
	Preview        string `json:"preview"`
}

// TaskBoard 读任务板；capability 为空则不过滤。板上条目即外部智能体发布的协作任务。
func (c *Client) TaskBoard(ctx context.Context, capability string) (*TaskBoardData, error) {
	args := []string{"task", "board"}
	if capability != "" {
		args = append(args, "--capability", capability)
	}
	env, err := c.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var d TaskBoardData
	if len(env.Data) > 0 {
		if e := json.Unmarshal(env.Data, &d); e != nil {
			return nil, fmt.Errorf("bolloon task board: 解码 data: %w", e)
		}
	}
	return &d, nil
}

// ── 信封透传方法（data schema 未在此固定，调用方按需 json.Unmarshal(env.Data)） ──
// 这些是"外部多智能体交互"与"调用 BOLLOON cli"的主要动作面。

// NetworkJoin 凭 networkLink 加入一个外部服务器的网络（需求 2：与外部智能体联网）。
func (c *Client) NetworkJoin(ctx context.Context, link string) (*Envelope, error) {
	return c.Run(ctx, "network", "join", link)
}

// AgentRegister 把"核聚变"系统注册为一个 BOLLOON agent（需求 2/3：对外暴露自身）。
// manifest 是 manifest 文件路径。
func (c *Client) AgentRegister(ctx context.Context, manifest string) (*Envelope, error) {
	return c.Run(ctx, "agent", "register", manifest)
}

// AgentDiscover 发现网络上的其它 agent（需求 2：枚举外部智能体）。
func (c *Client) AgentDiscover(ctx context.Context) (*Envelope, error) {
	return c.Run(ctx, "agent", "discover")
}

// TaskPublish 发布一条协作任务给外部智能体（需求 2：委派工作）。
// annPath 是公告文件路径。
func (c *Client) TaskPublish(ctx context.Context, annPath string) (*Envelope, error) {
	return c.Run(ctx, "task", "publish", annPath)
}

// TaskClaim 认领一条开放任务（需求 2：接外部智能体的活）。
func (c *Client) TaskClaim(ctx context.Context, announcementID string) (*Envelope, error) {
	return c.Run(ctx, "task", "claim", announcementID)
}

// TaskSend 向某条任务的参与者发消息（需求 2：与外部智能体通信，announcementID + 消息）。
func (c *Client) TaskSend(ctx context.Context, announcementID, message string) (*Envelope, error) {
	return c.Run(ctx, "task", "send", announcementID, message)
}

// EngineRun 把一段 prompt 委派给外部编码智能体执行（需求 2：借外部 agent 算力）。
func (c *Client) EngineRun(ctx context.Context, prompt string) (*Envelope, error) {
	return c.Run(ctx, "engine", "run", prompt)
}

// P2P 读取/启动本机 P2P 节点（让核聚变系统能被动接收外部智能体消息）。
func (c *Client) P2P(ctx context.Context) (*Envelope, error) {
	return c.Run(ctx, "p2p")
}

// ── 小工具 ────────────────────────────────────────────────────────────────

func hasArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == '\n' || b[start] == '\r' || b[start] == ' ' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == ' ' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}
