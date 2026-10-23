// Package identity 提供去中心化身份（DID）与可验证授权票据。
//
// 学 Hermes/Crush 的身份层，但按本仓纪律实现：
//
//   - 零第三方依赖 —— keypair 用标准库 crypto/ed25519，base58 用 math/big 自实现
//     （标准库没有 base58，而 did:key 规范要求 base58btc 编码，故手写 ~30 行）。
//   - did:key 合规 —— 标识 = "did:key:z" + base58btc( multicodec 前缀 0xed 0x01 ‖ 32 字节
//     ed25519 公钥 )，即规范里的 z6Mk… 形式，可被外部工具互操作。
//   - 凭据永不落库 —— 私钥只进内存；本包不提供任何"保存私钥到磁盘"的入口。
//   - fail-closed —— 签名/票据验不过一律返回 error，调用方必须显式处理。
//
// 能力令牌（capability token）是一张由签发者私钥签名的短期授权票据：
// {iss, sub, caps[], exp}，base64url(JSON) + "." + base64url(sig)。
// 它不是 JWT（无 header/alg 协商，算法固定 ed25519），避免 JWT 的 alg 混淆面。
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// base58btc 字母表（Bitcoin alphabet，did:key 规定用它）。
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Radix = big.NewInt(58)

// base58Encode 把字节编成 base58btc（含前导零字节 → '1' 的处理）。
func base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	digits := make([]byte, 0, len(b)*138/100+1)
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base58Radix, mod)
		digits = append(digits, base58Alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		digits = append(digits, base58Alphabet[0])
	}
	// 反转
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// base58Decode 是 base58Encode 的逆；非法字符 fail-closed。
func base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("identity: base58 空串")
	}
	n := new(big.Int)
	for _, r := range s {
		idx := strings.IndexRune(base58Alphabet, r)
		if idx < 0 {
			return nil, fmt.Errorf("identity: base58 非法字符 %q", r)
		}
		n.Mul(n, base58Radix)
		n.Add(n, big.NewInt(int64(idx)))
	}
	raw := n.Bytes()
	// 前导 '1' → 前导零字节
	pad := 0
	for _, r := range s {
		if r != '1' {
			break
		}
		pad++
	}
	if pad > 0 {
		raw = append(make([]byte, pad), raw...)
	}
	return raw, nil
}

// ed25519MulticodecPrefix 是 ed25519 公钥的 multicodec 前缀（0xed 0x01）。
var ed25519MulticodecPrefix = []byte{0xed, 0x01}

const didPrefix = "did:key:z"

// Identity 是一套 DID 身份（私钥 + 派生 DID）。
type Identity struct {
	priv ed25519.PrivateKey
	did  string
}

// Generate 新建一套随机身份。
func Generate() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: 生成 keypair 失败: %w", err)
	}
	return fromPrivate(priv)
}

// FromPrivateKey 从既有 ed25519 私钥恢复身份（用于从 KMS/HSM 注入）。
func FromPrivateKey(priv ed25519.PrivateKey) (*Identity, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: 私钥长度 %d，应为 %d", len(priv), ed25519.PrivateKeySize)
	}
	return fromPrivate(priv)
}

func fromPrivate(priv ed25519.PrivateKey) (*Identity, error) {
	pub := priv.Public().(ed25519.PublicKey)
	did, err := DIDFromPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv, did: did}, nil
}

// DID 返回 did:key:z6Mk… 形式标识。
func (id *Identity) DID() string { return id.did }

// PublicKey 返回公钥（给别人验证用）。
func (id *Identity) PublicKey() ed25519.PublicKey {
	return id.priv.Public().(ed25519.PublicKey)
}

// Sign 对消息签名（ed25519，纯确定性，无随机数隐患）。
func (id *Identity) Sign(msg []byte) []byte {
	return ed25519.Sign(id.priv, msg)
}

// DIDFromPublicKey 由公钥派生 did:key。
func DIDFromPublicKey(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("identity: 公钥长度 %d，应为 %d", len(pub), ed25519.PublicKeySize)
	}
	buf := make([]byte, 0, len(ed25519MulticodecPrefix)+len(pub))
	buf = append(buf, ed25519MulticodecPrefix...)
	buf = append(buf, pub...)
	return didPrefix + base58Encode(buf), nil
}

// PublicKeyFromDID 从 did:key 解析出公钥（验签/验票据用）。
func PublicKeyFromDID(did string) (ed25519.PublicKey, error) {
	if !strings.HasPrefix(did, didPrefix) {
		return nil, fmt.Errorf("identity: 不是 did:key 标识: %q", did)
	}
	raw, err := base58Decode(strings.TrimPrefix(did, didPrefix))
	if err != nil {
		return nil, err
	}
	if len(raw) != len(ed25519MulticodecPrefix)+ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: did:key 解码长度 %d 非法", len(raw))
	}
	if subtle.ConstantTimeCompare(raw[:2], ed25519MulticodecPrefix) != 1 {
		return nil, errors.New("identity: did:key multicodec 前缀不是 ed25519 (0xed01)")
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, raw[2:])
	return pub, nil
}

// Verify 用 DID 对应的公钥验证签名（fail-closed）。
func Verify(did string, msg, sig []byte) error {
	pub, err := PublicKeyFromDID(did)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("identity: 签名验不过")
	}
	return nil
}

// Capability 是一张能力令牌的声明体（被签名的部分）。
type Capability struct {
	Issuer   string   `json:"iss"`           // 签发者 DID
	Subject  string   `json:"sub"`           // 被授权者 DID（"*" = 任意）
	Caps     []string `json:"caps"`          // 能力名，如 "kernel.write" / "lsp.query"
	Audience string   `json:"aud,omitempty"` // 限定使用方 DID（空 = 不限）
	IssuedAt int64    `json:"iat"`           // unix 秒
	Expires  int64    `json:"exp"`           // unix 秒（0 = 不过期，测试/内部用）
}

// CapabilityToken 是 "base64url(JSON(claims)).base64url(sig)"。
type CapabilityToken string

// Issue 签发一张能力令牌。
func (id *Identity) Issue(subject string, caps []string, ttl time.Duration) (CapabilityToken, error) {
	if subject == "" {
		return "", errors.New("identity: subject 不能为空")
	}
	if len(caps) == 0 {
		return "", errors.New("identity: caps 不能为空（不给空权票据）")
	}
	now := time.Now()
	cap := Capability{
		Issuer:   id.did,
		Subject:  subject,
		Caps:     append([]string{}, caps...),
		IssuedAt: now.Unix(),
	}
	if ttl < 0 {
		return "", errors.New("identity: TTL 不能为负（不过期请传 0）")
	}
	if ttl > 0 {
		cap.Expires = now.Add(ttl).Unix()
	}
	return signCapability(id.priv, cap)
}

func signCapability(priv ed25519.PrivateKey, cap Capability) (CapabilityToken, error) {
	body, err := json.Marshal(cap)
	if err != nil {
		return "", fmt.Errorf("identity: 序列化票据失败: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	sig := ed25519.Sign(priv, []byte(payload))
	return CapabilityToken(payload + "." + base64.RawURLEncoding.EncodeToString(sig)), nil
}

// ParseCapability 解析并验签（不检查有效期/能力，只保证真实未被篡改）。
func ParseCapability(tok CapabilityToken) (Capability, error) {
	parts := strings.Split(string(tok), ".")
	if len(parts) != 2 {
		return Capability{}, errors.New("identity: 票据格式错（应 payload.sig）")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Capability{}, fmt.Errorf("identity: 票据 payload 解码失败: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Capability{}, fmt.Errorf("identity: 票据签名解码失败: %w", err)
	}
	var cap Capability
	if err := json.Unmarshal(body, &cap); err != nil {
		return Capability{}, fmt.Errorf("identity: 票据 JSON 解析失败: %w", err)
	}
	if cap.Issuer == "" {
		return Capability{}, errors.New("identity: 票据缺 issuer")
	}
	if err := Verify(cap.Issuer, []byte(parts[0]), sig); err != nil {
		return Capability{}, err
	}
	return cap, nil
}

// Authorize 校验一张票据是否授予 subject 某能力（fail-closed：任一项不满足即拒）。
func Authorize(tok CapabilityToken, subject, need string, now time.Time) (Capability, error) {
	cap, err := ParseCapability(tok)
	if err != nil {
		return cap, err
	}
	if cap.Subject != "*" && cap.Subject != subject {
		return cap, fmt.Errorf("identity: 票据不授予 %q（授予 %q）", subject, cap.Subject)
	}
	if cap.Expires != 0 && now.Unix() > cap.Expires {
		return cap, errors.New("identity: 票据已过期")
	}
	if !HasCap(cap, need) {
		return cap, fmt.Errorf("identity: 票据不含能力 %q", need)
	}
	return cap, nil
}

// HasCap 报告票据是否含某能力（支持 "kernel.*" 前缀通配）。
func HasCap(cap Capability, need string) bool {
	for _, c := range cap.Caps {
		if c == need || c == "*" {
			return true
		}
		if strings.HasSuffix(c, ".*") && strings.HasPrefix(need, strings.TrimSuffix(c, "*")) {
			return true
		}
	}
	return false
}
