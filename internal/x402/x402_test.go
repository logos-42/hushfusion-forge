package x402

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/wallet"
)

const testPayTo = "0x0000000000000000000000000000000000000001"

func TestParseAndSelect(t *testing.T) {
	body := []byte(`{"x402Version":1,"accepts":[
		{"scheme":"exact","network":"base","maxAmountRequired":"10000","payTo":"` + testPayTo + `","asset":"` + USDCAsset("base") + `"},
		{"scheme":"exact","network":"base-sepolia","maxAmountRequired":"20000","payTo":"` + testPayTo + `"}
	]}`)
	pr, err := ParsePaymentRequired(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := Select(pr.Accepts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Network != "base" || req.MaxAmountRequired != "10000" {
		t.Fatalf("选择结果不对: %+v", req)
	}
}

func TestBuildPaymentAndRecover(t *testing.T) {
	w, err := wallet.Create(t.TempDir(), wallet.BaseMainnet)
	if err != nil {
		t.Fatal(err)
	}
	req := PaymentRequirements{
		Scheme:            "exact",
		Network:           "base",
		MaxAmountRequired: "10000",
		PayTo:             testPayTo,
		Asset:             USDCAsset("base"),
		MaxTimeoutSeconds: 60,
		Extra:             map[string]any{"name": "USD Coin", "version": "2"},
	}
	header, err := BuildPayment(w, req, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodePayment(header)
	if err != nil {
		t.Fatal(err)
	}
	p := payload["payload"].(map[string]any)
	auth := p["authorization"].(map[string]any)
	if auth["from"] != w.Address || auth["to"] != testPayTo || auth["value"] != "10000" {
		t.Fatalf("authorization 不对: %+v", auth)
	}
	if auth["validBefore"] != "1700000060" {
		t.Fatalf("validBefore = %v", auth["validBefore"])
	}

	// 用同一 authorization 重算 EIP-712 摘要，验签恢复出钱包地址。
	nonceHex := strings.TrimPrefix(auth["nonce"].(string), "0x")
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil || len(nonceBytes) != 32 {
		t.Fatalf("nonce 不对: %v (%s)", err, nonceHex)
	}
	var nonce [32]byte
	copy(nonce[:], nonceBytes)
	value, _ := new(big.Int).SetString(auth["value"].(string), 10)
	validBefore, _ := new(big.Int).SetString(auth["validBefore"].(string), 10)
	digest, err := TransferWithAuthorizationDigest(Domain{
		Name: "USD Coin", Version: "2", ChainID: wallet.BaseMainnet, VerifyingContract: USDCAsset("base"),
	}, w.Address, testPayTo, value, big.NewInt(0), validBefore, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 32 {
		t.Fatalf("digest 长度 = %d", len(digest))
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(p["signature"].(string), "0x"))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := wallet.RecoverAddress(digest, sig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(recovered, w.Address) {
		t.Fatalf("EIP-712 恢复地址 %q != %q", recovered, w.Address)
	}
}
