// Package wallet 提供 HUSHFUSION Forge 的 EVM 加密钱包：
// secp256k1 私钥、0x 地址、did:pkh 去中心化身份，以及 EIP-712 哈希签名。
//
// 存储：调用方给一个专用目录（forge 用 ~/.forge/wallet/），文件 wallet.json，
// 权限 0600，目录 0700。私钥只在本包内使用；对外只暴露地址 / DID / 签名。
package wallet

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

const (
	// BaseMainnet / BaseSepolia 是 x402 最常用的两条 EVM 网络。
	BaseMainnet int64 = 8453
	BaseSepolia int64 = 84532
)

// Wallet 是落盘的钱包（私钥 hex 存在同一文件，0600）。
type Wallet struct {
	Version    int    `json:"version"`
	Address    string `json:"address"`
	DID        string `json:"did"`
	ChainID    int64  `json:"chain_id"`
	PrivateKey string `json:"private_key"`
	CreatedAt  string `json:"created_at"`

	priv *secp.PrivateKey
}

func DirPath(dir string) string  { return filepath.Join(dir, "wallet.json") }
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// Create 在 dir 下创建钱包；已存在则直接返回（不覆盖，避免丢了私钥/资金）。
func Create(dir string, chainID int64) (*Wallet, error) {
	if w, err := Load(dir); err == nil {
		return w, nil
	}
	if chainID == 0 {
		chainID = BaseMainnet
	}
	if err := EnsureDir(dir); err != nil {
		return nil, err
	}
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("wallet: 生成私钥失败: %w", err)
	}
	w := &Wallet{
		Version:    1,
		Address:    AddressFromPrivateKey(priv),
		ChainID:    chainID,
		PrivateKey: hex.EncodeToString(priv.Serialize()),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		priv:       priv,
	}
	w.DID = w.did()
	if err := w.save(dir); err != nil {
		return nil, err
	}
	return w, nil
}

// Load 读取钱包并校验地址与私钥一致。
func Load(dir string) (*Wallet, error) {
	b, err := os.ReadFile(DirPath(dir))
	if err != nil {
		return nil, fmt.Errorf("wallet: 未找到钱包（%s），先调用 wallet.create", DirPath(dir))
	}
	var w Wallet
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("wallet: 文件解析失败: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(w.PrivateKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("wallet: 私钥不是 hex: %w", err)
	}
	priv := secp.PrivKeyFromBytes(raw)
	if len(priv.Serialize()) != 32 {
		return nil, fmt.Errorf("wallet: 私钥长度不对")
	}
	addr := AddressFromPrivateKey(priv)
	if w.Address != "" && !strings.EqualFold(w.Address, addr) {
		return nil, fmt.Errorf("wallet: 地址 %s 与私钥推导的 %s 不一致", w.Address, addr)
	}
	w.Address = addr
	if w.DID == "" {
		w.DID = w.did()
	}
	w.priv = priv
	return &w, nil
}

func (w *Wallet) save(dir string) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp := DirPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, DirPath(dir))
}

func (w *Wallet) did() string {
	return fmt.Sprintf("did:pkh:eip155:%d:%s", w.ChainID, strings.ToLower(w.Address))
}

// DIDString 返回 did:pkh:eip155:<chain>:<address>（CAIP-10，DIAP 可直接用）。
func (w *Wallet) DIDString() string {
	if w.DID != "" {
		return w.DID
	}
	return w.did()
}

// SignHash 对 32 字节哈希做 secp256k1 签名，返回 65 字节
// （r || s || v，v 为 27/28，Ethereum 可直接用）。
func (w *Wallet) SignHash(hash []byte) ([]byte, error) {
	if w.priv == nil {
		return nil, fmt.Errorf("wallet: 私钥未加载")
	}
	if len(hash) != 32 {
		return nil, fmt.Errorf("wallet: 待签哈希必须是 32 字节，got %d", len(hash))
	}
	compact := ecdsa.SignCompact(w.priv, hash, false) // header(27/28) || R || S
	out := make([]byte, 65)
	copy(out[:64], compact[1:]) // r || s
	out[64] = compact[0]        // v = 27/28（Ethereum 习惯）
	return out, nil
}

// SignHex 返回 0x 开头的 65 字节签名。
func (w *Wallet) SignHex(hash []byte) (string, error) {
	sig, err := w.SignHash(hash)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// AddressFromPrivateKey 派生 0x 地址（keccak256(uncompressed pubkey[1:])[12:]）。
func AddressFromPrivateKey(priv *secp.PrivateKey) string {
	return AddressFromPub(priv.PubKey())
}

// AddressFromPub 从公钥派生地址。
func AddressFromPub(pub *secp.PublicKey) string {
	raw := pub.SerializeUncompressed() // 65 字节，首字节 0x04
	h := Keccak256(raw[1:])
	return "0x" + hex.EncodeToString(h[12:])
}

// RecoverAddress 从 Ethereum 格式（r||s||v）签名与哈希恢复地址（测试/验签用）。
func RecoverAddress(hash, sig []byte) (string, error) {
	if len(sig) != 65 {
		return "", fmt.Errorf("wallet: 签名必须是 65 字节，got %d", len(sig))
	}
	compact := make([]byte, 65)
	compact[0] = sig[64]
	copy(compact[1:], sig[:64])
	pub, _, err := ecdsa.RecoverCompact(compact, hash)
	if err != nil {
		return "", err
	}
	return AddressFromPub(pub), nil
}

// Keccak256 是 Ethereum 使用的 legacy Keccak（不是 NIST SHA3）。
func Keccak256(b ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, part := range b {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

// RandomNonce 生成 32 字节随机 nonce。
func RandomNonce() ([32]byte, error) {
	var out [32]byte
	_, err := rand.Read(out[:])
	return out, err
}

// ParseUnits 把十进制字符串按 decimals 转成最小单位（如 "0.10" + 6 → 100000）。
func ParseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("金额为空")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.SplitN(s, ".", 2)
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("金额 %q 小数位超过 %d", s, decimals)
	}
	for len(frac) < decimals {
		frac += "0"
	}
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, fmt.Errorf("金额 %q 不是数字", s)
	}
	if neg {
		n.Neg(n)
	}
	return n, nil
}

// FormatUnits 把最小单位转成十进制字符串。
func FormatUnits(n *big.Int, decimals int) string {
	if n == nil {
		return "0"
	}
	s := n.String()
	if decimals == 0 {
		return s
	}
	for len(s) <= decimals {
		s = "0" + s
	}
	return s[:len(s)-decimals] + "." + s[len(s)-decimals:]
}
