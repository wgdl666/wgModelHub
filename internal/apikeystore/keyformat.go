package apikeystore

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const tokenPrefix = "wgmh_"

// HashSecret 对调用方持有的 secret 段做 SHA-256，供 constant-time 比对。
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretsMatch 使用 constant-time compare，避免通过时序侧信道推断 hash 前缀。
func SecretsMatch(storedHex, presentedSecret string) bool {
	stored, err := hex.DecodeString(strings.TrimSpace(storedHex))
	if err != nil {
		return false
	}
	presented := sha256.Sum256([]byte(presentedSecret))
	return subtle.ConstantTimeCompare(stored, presented[:]) == 1
}

// ParseBearerToken 从 authorization metadata 解析标准 Bearer scheme 后的 wgmh_<key_id>_<secret>。
// 不接受无 Bearer 前缀的裸 Key，避免与 HTTP 客户端默认行为混淆。
func ParseBearerToken(raw string) (keyID, secret string, ok bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 7 || !strings.EqualFold(raw[:7], "bearer ") {
		return "", "", false
	}
	raw = strings.TrimSpace(raw[7:])
	if !strings.HasPrefix(raw, tokenPrefix) {
		return "", "", false
	}
	body := strings.TrimPrefix(raw, tokenPrefix)
	keyID, secret, found := strings.Cut(body, "_")
	keyID = strings.TrimSpace(keyID)
	secret = strings.TrimSpace(secret)
	if !found || keyID == "" || secret == "" {
		return "", "", false
	}
	return keyID, secret, true
}
