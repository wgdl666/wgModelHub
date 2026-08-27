package apikeystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/modelhubapikey"
)

var (
	ErrInvalid = errors.New("api key invalid")
)

// Principal 是公网鉴权成功后写入 context 的稳定调用主体。
type Principal struct {
	PrincipalID string
	KeyID       string
}

// Store 经 Ent 访问 modelhub_api_key；公网 RPC 按 key_id 直查以保证吊销即时生效。
// Key 创建/轮换/吊销仅由 wgDevPlatform 管理面执行，本包只保留鉴权所需最小逻辑。
type Store struct {
	client *ent.Client
}

func New(client *ent.Client) *Store {
	return &Store{client: client}
}

// Authenticate 校验 Bearer token；任何缺失/格式/状态/hash 问题统一返回 ErrInvalid，避免泄露原因。
func (s *Store) Authenticate(ctx context.Context, authorization string) (Principal, error) {
	if s == nil || s.client == nil {
		return Principal{}, ErrInvalid
	}
	keyID, secret, ok := ParseBearerToken(authorization)
	if !ok {
		return Principal{}, ErrInvalid
	}
	row, err := s.client.ModelhubAPIKey.Query().
		Where(modelhubapikey.KeyIDEQ(keyID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return Principal{}, ErrInvalid
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load api key: %w", err)
	}
	now := time.Now().UTC()
	if row.RevokedAt != nil || !row.ExpiresAt.After(now) {
		return Principal{}, ErrInvalid
	}
	if !SecretsMatch(row.SecretSha256, secret) {
		return Principal{}, ErrInvalid
	}
	return Principal{PrincipalID: row.PrincipalID, KeyID: row.KeyID}, nil
}
