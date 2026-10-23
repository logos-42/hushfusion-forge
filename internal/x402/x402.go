// Package x402 实现 x402 协议的客户端侧：
// 解析 HTTP 402 的 accepts、按 exact 方案构造 EIP-3009
// TransferWithAuthorization、EIP-712 签名、带 X-PAYMENT 头重试。
//
// 只做客户端；服务端验签/结算由 x402 facilitator 负责。
package x402

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/wallet"
)

// PaymentRequirements 是 402 响应里 accepts[] 的一项。
type PaymentRequirements struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`
	MaxAmountRequired string         `json:"maxAmountRequired"`
	Resource          string         `json:"resource"`
	Description       string         `json:"description,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds,omitempty"`
	Asset             string         `json:"asset,omitempty"`
	Extra             map[string]any `json:"extra,omitempty"`
}

// PaymentRequired 是 402 响应体。
type PaymentRequired struct {
	X402Version int                   `json:"x402Version"`
	Error       string                `json:"error,omitempty"`
	Accepts     []PaymentRequirements `json:"accepts"`
}

// ParsePaymentRequired 解析 402 响应体。
func ParsePaymentRequired(body []byte) (*PaymentRequired, error) {
	var pr PaymentRequired
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("x402: 402 响应不是 JSON: %w", err)
	}
	if len(pr.Accepts) == 0 {
		return nil, fmt.Errorf("x402: 402 响应没有 accepts")
	}
	return &pr, nil
}

// Select 选择 exact + Base 的报价（没有则退回第一条 exact）。
func Select(reqs []PaymentRequirements) (*PaymentRequirements, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("x402: 没有可接受的支付方式")
	}
	for i := range reqs {
		r := &reqs[i]
		if r.Scheme == "exact" && (r.Network == "base" || r.Network == "base-sepolia") {
			return r, nil
		}
	}
	for i := range reqs {
		if reqs[i].Scheme == "exact" {
			return &reqs[i], nil
		}
	}
	return &reqs[0], nil
}

// NetworkChainID 把 x402 network 映射为 EVM chainId。
func NetworkChainID(network string) (int64, string, error) {
	switch strings.ToLower(network) {
	case "base":
		return wallet.BaseMainnet, "USD Coin", nil
	case "base-sepolia":
		return wallet.BaseSepolia, "USD Coin", nil
	default:
		return 0, "", fmt.Errorf("x402: 暂不支持的 network %q（支持 base / base-sepolia）", network)
	}
}

// USDCAsset 返回该网络的 USDC 合约地址。
func USDCAsset(network string) string {
	switch strings.ToLower(network) {
	case "base":
		return "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	case "base-sepolia":
		return "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	}
	return ""
}

// BuildPayment 按 exact/EIP-3009 构造 X-PAYMENT 头的 base64 值。
func BuildPayment(w *wallet.Wallet, req PaymentRequirements, now time.Time) (string, error) {
	if w == nil {
		return "", fmt.Errorf("x402: 钱包为空")
	}
	chainID, defaultName, err := NetworkChainID(req.Network)
	if err != nil {
		return "", err
	}
	asset := strings.TrimSpace(req.Asset)
	if asset == "" {
		asset = USDCAsset(req.Network)
	}
	if asset == "" {
		return "", fmt.Errorf("x402: 缺 asset（USDC 合约地址）")
	}
	value, ok := new(big.Int).SetString(strings.TrimSpace(req.MaxAmountRequired), 10)
	if !ok || value.Sign() <= 0 {
		return "", fmt.Errorf("x402: maxAmountRequired %q 不是正整数", req.MaxAmountRequired)
	}
	to := strings.TrimSpace(req.PayTo)
	if !isAddress(to) {
		return "", fmt.Errorf("x402: payTo %q 不是合法地址", to)
	}
	timeout := req.MaxTimeoutSeconds
	if timeout <= 0 {
		timeout = 600
	}
	validBefore := big.NewInt(now.Unix() + int64(timeout))

	nonce, err := wallet.RandomNonce()
	if err != nil {
		return "", err
	}
	name, version := defaultName, "2"
	if v, ok := req.Extra["name"].(string); ok && v != "" {
		name = v
	}
	if v, ok := req.Extra["version"].(string); ok && v != "" {
		version = v
	}

	digest, err := TransferWithAuthorizationDigest(Domain{
		Name: name, Version: version, ChainID: chainID, VerifyingContract: asset,
	}, w.Address, to, value, big.NewInt(0), validBefore, nonce)
	if err != nil {
		return "", err
	}
	sig, err := w.SignHex(digest)
	if err != nil {
		return "", err
	}

	payload := map[string]any{
		"x402Version": 1,
		"scheme":      "exact",
		"network":     req.Network,
		"payload": map[string]any{
			"signature": sig,
			"authorization": map[string]string{
				"from":        w.Address,
				"to":          to,
				"value":       value.String(),
				"validAfter":  "0",
				"validBefore": validBefore.String(),
				"nonce":       "0x" + hex.EncodeToString(nonce[:]),
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecodePayment 解码 X-PAYMENT（测试/调试用）。
func DecodePayment(header string) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Domain 是 EIP-712 域。
type Domain struct {
	Name              string
	Version           string
	ChainID           int64
	VerifyingContract string
}

// TransferWithAuthorizationDigest 计算 EIP-3009 的 EIP-712 摘要。
func TransferWithAuthorizationDigest(d Domain, from, to string, value, validAfter, validBefore *big.Int, nonce [32]byte) ([]byte, error) {
	domainSep, err := HashDomain(d)
	if err != nil {
		return nil, err
	}
	structHash, err := HashTransferWithAuthorization(from, to, value, validAfter, validBefore, nonce)
	if err != nil {
		return nil, err
	}
	return wallet.Keccak256([]byte{0x19, 0x01}, domainSep, structHash), nil
}

// HashDomain 计算 EIP-712 domainSeparator。
func HashDomain(d Domain) ([]byte, error) {
	contract, err := address32(d.VerifyingContract)
	if err != nil {
		return nil, err
	}
	typeHash := wallet.Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	return wallet.Keccak256(
		typeHash,
		wallet.Keccak256([]byte(d.Name)),
		wallet.Keccak256([]byte(d.Version)),
		uint256(new(big.Int).SetInt64(d.ChainID)),
		contract,
	), nil
}

// HashTransferWithAuthorization 计算结构体哈希。
func HashTransferWithAuthorization(from, to string, value, validAfter, validBefore *big.Int, nonce [32]byte) ([]byte, error) {
	fromB, err := address32(from)
	if err != nil {
		return nil, err
	}
	toB, err := address32(to)
	if err != nil {
		return nil, err
	}
	typeHash := wallet.Keccak256([]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))
	return wallet.Keccak256(
		typeHash, fromB, toB,
		uint256(value), uint256(validAfter), uint256(validBefore), nonce[:],
	), nil
}

func uint256(n *big.Int) []byte {
	out := make([]byte, 32)
	if n == nil || n.Sign() < 0 {
		return out
	}
	b := n.Bytes()
	copy(out[32-len(b):], b)
	return out
}

func isAddress(s string) bool {
	s = strings.TrimPrefix(s, "0x")
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func address32(s string) ([]byte, error) {
	if !isAddress(s) {
		return nil, fmt.Errorf("x402: 非法地址 %q", s)
	}
	raw, _ := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	out := make([]byte, 32)
	copy(out[12:], raw)
	return out, nil
}

// Result 是一次 x402 请求的结果。
type Result struct {
	Status          int
	Body            []byte
	Paid            bool
	PaymentResponse string
	Requirements    *PaymentRequired
}

// Fetch 请求 URL（GET）；若返回 402 则按 maxAmount 上限签名并重试一次。
// maxAmount 为 nil 表示不设上限（调用方应先设好上限）。
func Fetch(ctx context.Context, client *http.Client, rawURL string, w *wallet.Wallet, maxAmount *big.Int) (*Result, error) {
	return FetchRequest(ctx, client, http.MethodGet, rawURL, nil, nil, w, maxAmount)
}

// FetchRequest 支持任意 method/body/headers，其余同 Fetch。
func FetchRequest(ctx context.Context, client *http.Client, method, rawURL string, body []byte, headers map[string]string, w *wallet.Wallet, maxAmount *big.Int) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("x402: 只支持 http/https URL")
	}
	if method == "" {
		method = http.MethodGet
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := do(ctx, client, method, rawURL, body, headers, "")
	if err != nil {
		return nil, err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		return &Result{Status: resp.StatusCode, Body: respBody}, nil
	}
	pr, err := ParsePaymentRequired(respBody)
	if err != nil {
		return nil, err
	}
	req, err := Select(pr.Accepts)
	if err != nil {
		return nil, err
	}
	value, ok := new(big.Int).SetString(req.MaxAmountRequired, 10)
	if !ok {
		return nil, fmt.Errorf("x402: 金额 %q 非法", req.MaxAmountRequired)
	}
	if maxAmount != nil && value.Cmp(maxAmount) > 0 {
		return nil, fmt.Errorf("x402: 价格 %s 超过上限 %s（最小单位）", value, maxAmount)
	}
	header, err := BuildPayment(w, *req, time.Now())
	if err != nil {
		return nil, err
	}
	resp2, err := do(ctx, client, method, rawURL, body, headers, header)
	if err != nil {
		return nil, err
	}
	body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	_ = resp2.Body.Close()
	return &Result{
		Status:          resp2.StatusCode,
		Body:            body2,
		Paid:            true,
		PaymentResponse: resp2.Header.Get("X-PAYMENT-RESPONSE"),
		Requirements:    pr,
	}, nil
}

func do(ctx context.Context, client *http.Client, method, rawURL string, body []byte, headers map[string]string, payment string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if payment != "" {
		req.Header.Set("X-PAYMENT", payment)
	}
	return client.Do(req)
}
