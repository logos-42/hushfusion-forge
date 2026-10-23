package wallet

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestCreateLoadSignRecover(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir, BaseMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^0x[0-9a-f]{40}$`).MatchString(w.Address) {
		t.Fatalf("地址格式不对: %q", w.Address)
	}
	if got, want := w.DIDString(), "did:pkh:eip155:8453:"+strings.ToLower(w.Address); got != want {
		t.Fatalf("DID = %q, want %q", got, want)
	}
	if info, err := os.Stat(DirPath(dir)); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("钱包文件权限应为 0600, got %o", perm)
	}

	// 再次 Create 不应覆盖已有钱包。
	w2, err := Create(dir, BaseMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Address != w.Address {
		t.Fatalf("重复 create 覆盖了钱包: %s → %s", w.Address, w2.Address)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	hash := Keccak256([]byte("hushfusion-wallet-test"))
	sig, err := loaded.SignHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 65 {
		t.Fatalf("签名长度 = %d", len(sig))
	}
	recovered, err := RecoverAddress(hash, sig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(recovered, w.Address) {
		t.Fatalf("恢复地址 %q != %q", recovered, w.Address)
	}
}

func TestParseFormatUnits(t *testing.T) {
	n, err := ParseUnits("0.10", 6)
	if err != nil {
		t.Fatal(err)
	}
	if n.String() != "100000" {
		t.Fatalf("ParseUnits = %s", n)
	}
	if got := FormatUnits(n, 6); got != "0.100000" {
		t.Fatalf("FormatUnits = %q", got)
	}
	if _, err := ParseUnits("1.1234567", 6); err == nil {
		t.Fatal("超过小数位应报错")
	}
}
