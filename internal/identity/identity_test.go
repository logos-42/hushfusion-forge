package identity

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

func TestGenerateDIDFormat(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id.DID(), "did:key:z6Mk") {
		t.Fatalf("DID 应为 did:key:z6Mk… 形式, got %q", id.DID())
	}
	pub, err := PublicKeyFromDID(id.DID())
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(id.PublicKey()) {
		t.Fatal("从 DID 解析出的公钥与原公钥不一致")
	}
}

func TestSignVerify(t *testing.T) {
	id, _ := Generate()
	msg := []byte("写一条任务")
	sig := id.Sign(msg)
	if err := Verify(id.DID(), msg, sig); err != nil {
		t.Fatalf("本人签名应验过: %v", err)
	}
	if err := Verify(id.DID(), []byte("写两条任务"), sig); err == nil {
		t.Fatal("改消息后验签应失败")
	}
	other, _ := Generate()
	if err := Verify(other.DID(), msg, sig); err == nil {
		t.Fatal("非签发者 DID 验签应失败")
	}
}

func TestDIDRejectMalformed(t *testing.T) {
	for _, c := range []string{"", "did:web:example.com", "did:key:", "did:key:z!!!notbase58", "did:key:z2"} {
		if _, err := PublicKeyFromDID(c); err == nil {
			t.Fatalf("畸形 DID %q 应被拒", c)
		}
	}
}

func TestBase58RoundTrip(t *testing.T) {
	for _, b := range [][]byte{{0}, {0, 0, 1}, {255, 254, 0, 1}, {0xed, 0x01, 9, 9, 9}} {
		got, err := base58Decode(base58Encode(b))
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		if string(got) != string(b) {
			t.Fatalf("base58 往返不一致: %v → %v", b, got)
		}
	}
}

func TestCapabilityIssueAndAuthorize(t *testing.T) {
	iss, _ := Generate()
	sub, _ := Generate()
	tok, err := iss.Issue(sub.DID(), []string{"kernel.write", "lsp.query"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := Authorize(tok, sub.DID(), "kernel.write", now); err != nil {
		t.Fatalf("授权的能力应放行: %v", err)
	}
	if _, err := Authorize(tok, sub.DID(), "kernel.admin", now); err == nil {
		t.Fatal("未授予的能力应被拒")
	}
	other, _ := Generate()
	if _, err := Authorize(tok, other.DID(), "kernel.write", now); err == nil {
		t.Fatal("票据不授予第三方，应被拒")
	}
	short, _ := iss.Issue(sub.DID(), []string{"kernel.write"}, -time.Minute)
	if _, err := Authorize(short, sub.DID(), "kernel.write", now); err == nil {
		t.Fatal("过期票据应被拒")
	}
	tampered := CapabilityToken(strings.Replace(string(tok), "kernel.write", "kernel.admin", 1))
	if _, err := Authorize(tampered, sub.DID(), "kernel.admin", now); err == nil {
		t.Fatal("被篡改的票据应验签失败")
	}
}

func TestCapabilityWildcard(t *testing.T) {
	iss, _ := Generate()
	tok, _ := iss.Issue("*", []string{"kernel.*"}, time.Minute)
	c := mustParse(t, tok)
	if !HasCap(c, "kernel.write") {
		t.Fatal("kernel.* 应覆盖 kernel.write")
	}
	if HasCap(c, "lsp.query") {
		t.Fatal("kernel.* 不应覆盖 lsp.query")
	}
	if _, err := Authorize(tok, "did:key:zanyone", "kernel.write", time.Now()); err != nil {
		t.Fatalf("通配 subject 应放行: %v", err)
	}
}

func TestIssueRejectsEmptyCaps(t *testing.T) {
	iss, _ := Generate()
	if _, err := iss.Issue("did:key:zx", nil, time.Minute); err == nil {
		t.Fatal("空能力票据应被拒（不给空权）")
	}
	if _, err := iss.Issue("", []string{"a"}, time.Minute); err == nil {
		t.Fatal("空 subject 应被拒")
	}
}

func TestFromPrivateKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := FromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(id.DID(), []byte("x"), id.Sign([]byte("x"))); err != nil {
		t.Fatalf("恢复出的身份应能自签自验: %v", err)
	}
	if _, err := FromPrivateKey(priv[:10]); err == nil {
		t.Fatal("短私钥应被拒")
	}
}

func mustParse(t *testing.T, tok CapabilityToken) Capability {
	t.Helper()
	c, err := ParseCapability(tok)
	if err != nil {
		t.Fatalf("票据应可解析: %v", err)
	}
	return c
}
