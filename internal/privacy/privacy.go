// Package privacy 是智能体之间的隐私围栏。
//
// 目标不是“识别所有隐私”（做不到），而是默认拒绝最危险的一类：
// 私钥、API key/token、密码、含凭据的配置文件、常见个人标识。
// 任何进入内部商议的内容都必须先过 Redact；文件/命令工具对敏感路径直接 deny。
package privacy

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Finding 是一个被命中的隐私规则。
type Finding struct {
	Rule string
}

var secretRules = []struct {
	name string
	re   *regexp.Regexp
	repl string
}{
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[已隐藏私钥]"},
	{"bearer", regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`), "Bearer [已隐藏]"},
	{"openai-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`), "[已隐藏 API key]"},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), "[已隐藏 GitHub token]"},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), "[已隐藏 Slack token]"},
	{"aws-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[已隐藏 AWS key]"},
	{"assignment", regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|passwd|token|access[_-]?key)\s*[:=]\s*["']?[^\s"']{6,}`), "$1=[已隐藏]"},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), "[邮箱已隐藏]"},
	{"china-phone", regexp.MustCompile(`\b1[3-9]\d{9}\b`), "[手机号已隐藏]"},
	{"china-id", regexp.MustCompile(`\b\d{17}[\dXx]\b`), "[证件号已隐藏]"},
	{"ssh-path", regexp.MustCompile(`(?:~|/Users/[^/\s]+|/home/[^/\s]+)/\.ssh(?:/[^\s"']*)?`), "[敏感路径已隐藏]"},
}

var sensitiveDirs = map[string]bool{
	".ssh": true, ".aws": true, ".gnupg": true, ".kube": true,
	"keychains": true, "secrets": true, "wallet": true,
}

var sensitiveNames = map[string]bool{
	".env": true, ".netrc": true, ".npmrc": true, ".git-credentials": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"secrets.json": true, "credentials.json": true, "keychain.json": true,
	"wallet.dat": true, "login data": true, "cookies": true,
}

var sensitiveCommandParts = []string{
	"/.ssh/", "id_rsa", "id_ed25519", "authorized_keys",
	".aws/credentials", ".git-credentials", ".netrc", ".npmrc",
	"secrets.json", "wallet.dat", "keychain", "login data",
	".env", "security find-generic-password", "security find-internet-password",
}

// Redact 隐藏常见凭据/个人标识；返回脱敏文本与命中规则。
func Redact(text string) (string, []Finding) {
	out := text
	var findings []Finding
	for _, r := range secretRules {
		if r.re.MatchString(out) {
			findings = append(findings, Finding{Rule: r.name})
			out = r.re.ReplaceAllString(out, r.repl)
		}
	}
	return out, findings
}

// ContainsPrivateKey 是硬拒绝条件：私钥绝不进入内部商议。
func ContainsPrivateKey(text string) bool {
	return secretRules[0].re.MatchString(text)
}

// CheckInput 是用户消息进入会话前的门：
//   - 私钥/API key/GitHub token/AWS key/Bearer 等硬凭据：blocked=true，原文不落库；
//   - 邮箱/手机号/证件号等：只脱敏后返回，不阻断正常对话。
func CheckInput(text string) (clean string, findings []Finding, blocked bool) {
	if ContainsPrivateKey(text) {
		return "", []Finding{{Rule: "private-key"}}, true
	}
	clean, findings = Redact(text)
	for _, f := range findings {
		switch f.Rule {
		case "openai-key", "github-token", "slack-token", "aws-key", "bearer", "assignment":
			return "", findings, true
		}
	}
	return clean, findings, false
}

// IsHighRiskToolOutput 判断工具输出是否必须整段阻断（私钥绝不进上下文）。
func IsHighRiskToolOutput(text string) bool {
	return ContainsPrivateKey(text)
}

// SensitivePath 判断路径是否属于高敏感文件/目录。
// 只做 basename/路径段匹配，不读文件内容。
func SensitivePath(p string) bool {
	clean := filepath.Clean(strings.TrimSpace(p))
	if clean == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(clean))
	if sensitiveNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") {
		return true
	}
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(clean)), "/") {
		if sensitiveDirs[part] {
			return true
		}
	}
	return false
}

// SensitiveCommand 对 shell 的明显越界读取做 deny（不是完整 DLP）。
func SensitiveCommand(cmd string) bool {
	low := strings.ToLower(cmd)
	for _, part := range sensitiveCommandParts {
		if strings.Contains(low, part) {
			return true
		}
	}
	return false
}
