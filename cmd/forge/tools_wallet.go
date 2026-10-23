package main

// tools_wallet.go —— 加密钱包 + x402 协议（智能体之间交换信息/资源）。
//
// 钱包专用目录：~/.forge/wallet/wallet.json（0600，目录 0700）。
// DID：did:pkh:eip155:<chain>:<address>，可直接作为 DIAP 的 subject。
// x402：解析 HTTP 402 → exact/EIP-3009 签名 → 带 X-PAYMENT 重试。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/wallet"
	"github.com/logos-42/hushfusion-forge/internal/x402"
)

func walletDir(base string) string {
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_WALLET_DIR")); v != "" {
		return v
	}
	return filepath.Join(runtimeRoot(base), "wallet")
}

// currentWalletDID 返回钱包的 did:pkh（没有钱包时为空），供 DIAP 签发能力票据时使用。
func currentWalletDID(base string) string {
	w, err := wallet.Load(walletDir(base))
	if err != nil {
		return ""
	}
	return w.DIDString()
}

func walletInfo(w *wallet.Wallet, base string) map[string]any {
	return map[string]any{
		"address":    w.Address,
		"did":        w.DIDString(),
		"chain_id":   w.ChainID,
		"path":       wallet.DirPath(walletDir(base)),
		"created_at": w.CreatedAt,
	}
}

func registerWalletTools(s *agent.Session, base string) {
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("wallet.create", "创建/读取加密钱包（专用目录 ~/.forge/wallet）",
			map[string]any{
				"chain_id": map[string]any{"type": "integer", "description": "EVM chainId，缺省 8453（Base）"},
			}, nil),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			w, err := wallet.Create(walletDir(base), int64(argInt(args, "chain_id", int(wallet.BaseMainnet))))
			if err != nil {
				return "", err
			}
			return jsonText(walletInfo(w, base))
		},
	})

	register(agent.Tool{
		Spec: projectTool("wallet.info", "查看钱包地址 / DID / chain（绝不返回私钥）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			w, err := wallet.Load(walletDir(base))
			if err != nil {
				return "", err
			}
			return jsonText(walletInfo(w, base))
		},
	})

	register(agent.Tool{
		Spec: projectTool("wallet.balance", "查询钱包的 USDC 余额（Base 公共 RPC）",
			map[string]any{
				"rpc_url": map[string]any{"type": "string", "description": "可选：覆盖 RPC；默认 Base 公共节点"},
				"token":   map[string]any{"type": "string", "description": "可选：ERC-20 合约；默认 USDC"},
			}, nil),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			w, err := wallet.Load(walletDir(base))
			if err != nil {
				return "", err
			}
			token := argString(args, "token", x402.USDCAsset(networkName(w.ChainID)))
			rpc := argString(args, "rpc_url", defaultRPC(w.ChainID))
			bal, err := erc20Balance(ctx, rpc, token, w.Address)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{
				"wallet": w.Address, "did": w.DIDString(), "chain_id": w.ChainID,
				"token": token, "decimals": 6,
				"raw": bal.String(), "formatted": wallet.FormatUnits(bal, 6), "rpc": rpc,
			})
		},
	})

	register(agent.Tool{
		Spec: projectTool("x402.requirements", "读取一个 x402 资源的 402 报价（不付款）",
			map[string]any{"url": map[string]any{"type": "string"}}, []string{"url"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			u := argString(args, "url", "")
			if u == "" {
				return "", fmt.Errorf("x402.requirements: url 为空")
			}
			client := &http.Client{Timeout: 30 * time.Second}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return "", err
			}
			req.Header.Set("Accept", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != http.StatusPaymentRequired {
				return jsonText(map[string]any{"status": resp.StatusCode, "body": string(body)})
			}
			pr, err := x402.ParsePaymentRequired(body)
			if err != nil {
				return "", err
			}
			return jsonText(pr)
		},
	})

	register(agent.Tool{
		Spec: projectTool("x402.fetch", "请求 x402 资源；遇到 402 时在额度内自动付款并重试",
			map[string]any{
				"url":             map[string]any{"type": "string"},
				"method":          map[string]any{"type": "string", "description": "HTTP 方法，缺省 GET"},
				"body":            map[string]any{"type": "string", "description": "可选请求体"},
				"headers":         map[string]any{"type": "object", "description": "可选请求头"},
				"max_amount_usdc": map[string]any{"type": "string", "description": "单次付款上限（USDC），缺省 1"},
			}, []string{"url"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			u := argString(args, "url", "")
			if u == "" {
				return "", fmt.Errorf("x402.fetch: url 为空")
			}
			w, err := wallet.Load(walletDir(base))
			if err != nil {
				return "", fmt.Errorf("%w；先调用 wallet.create", err)
			}
			max, err := wallet.ParseUnits(argString(args, "max_amount_usdc", "1"), 6)
			if err != nil {
				return "", err
			}
			headers := map[string]string{}
			if m, ok := args["headers"].(map[string]any); ok {
				for k, v := range m {
					if s, ok := v.(string); ok {
						headers[k] = s
					}
				}
			}
			client := &http.Client{Timeout: 90 * time.Second}
			res, err := x402.FetchRequest(ctx, client, argString(args, "method", http.MethodGet),
				u, []byte(argString(args, "body", "")), headers, w, max)
			if err != nil {
				return "", err
			}
			out := map[string]any{
				"status": res.Status, "paid": res.Paid,
				"payment_response": res.PaymentResponse,
				"body":             string(res.Body),
			}
			if res.Requirements != nil {
				out["requires"] = res.Requirements
			}
			return jsonText(out)
		},
	})
}

func networkName(chainID int64) string {
	if chainID == wallet.BaseSepolia {
		return "base-sepolia"
	}
	return "base"
}

func defaultRPC(chainID int64) string {
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_RPC_URL")); v != "" {
		return v
	}
	if chainID == wallet.BaseSepolia {
		return "https://sepolia.base.org"
	}
	return "https://mainnet.base.org"
}

// erc20Balance 通过 eth_call 调 balanceOf(address)。
func erc20Balance(ctx context.Context, rpc, token, address string) (*big.Int, error) {
	if !strings.HasPrefix(token, "0x") || len(token) != 42 {
		return nil, fmt.Errorf("token %q 不是 ERC-20 地址", token)
	}
	addr := strings.TrimPrefix(strings.ToLower(address), "0x")
	data := "0x70a08231" + strings.Repeat("0", 24) + addr
	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_call",
		"params": []any{map[string]any{"to": token, "data": data}, "latest"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpc, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc: %s", out.Error.Message)
	}
	raw := strings.TrimPrefix(out.Result, "0x")
	if raw == "" {
		return new(big.Int), nil
	}
	n, ok := new(big.Int).SetString(raw, 16)
	if !ok {
		return nil, fmt.Errorf("rpc: 返回的余额不是 hex: %s", out.Result)
	}
	return n, nil
}
