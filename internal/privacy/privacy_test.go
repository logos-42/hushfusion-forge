package privacy

import "testing"

func TestRedactSecrets(t *testing.T) {
	in := "api_key=sk-abcdefghijklmnopqrstuvwxyz\nemail alice@example.com\nphone 13800138000"
	out, findings := Redact(in)
	for _, bad := range []string{"sk-abcdefghijklmnopqrstuvwxyz", "alice@example.com", "13800138000"} {
		if contains(out, bad) {
			t.Fatalf("未脱敏 %q: %s", bad, out)
		}
	}
	if len(findings) < 3 {
		t.Fatalf("命中规则不足: %+v", findings)
	}
}

func TestPrivateKeyBlocked(t *testing.T) {
	in := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	if !ContainsPrivateKey(in) {
		t.Fatal("私钥应硬拒绝")
	}
}

func TestSensitivePath(t *testing.T) {
	for _, p := range []string{"/Users/a/.ssh/id_rsa", "/tmp/.env", "/tmp/secrets.json", "/x/server.pem"} {
		if !SensitivePath(p) {
			t.Fatalf("应判敏感: %s", p)
		}
	}
	if SensitivePath("/tmp/notes.md") {
		t.Fatal("普通文件不应判敏感")
	}
}

func TestSensitiveCommand(t *testing.T) {
	if !SensitiveCommand("cat ~/.ssh/id_rsa") || !SensitiveCommand("cat .env") {
		t.Fatal("明显敏感命令应拒绝")
	}
	if SensitiveCommand("go test ./...") {
		t.Fatal("普通命令不应拒绝")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
