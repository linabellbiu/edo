package auth

import (
	"errors"
	"fmt"

	"github.com/matthewhartstonge/argon2"
)

var defaultPasswordConfig = &argon2.Config{
	HashLength:  32,
	SaltLength:  16,
	TimeCost:    3,
	MemoryCost:  64 * 1024,
	Parallelism: 2,
	Mode:        argon2.ModeArgon2id,
	Version:     argon2.Version13,
}

func HashPassword(password string) (string, error) {
	hash, err := defaultPasswordConfig.HashEncoded([]byte(password))
	if err != nil {
		return "", fmt.Errorf("生成密码摘要失败: %w", err)
	}
	return string(hash), nil
}

func ComparePassword(password, encoded string) (bool, error) {
	decoded, err := argon2.Decode([]byte(encoded))
	if err != nil {
		return false, errors.New("密码摘要格式无效")
	}
	// 第三方包负责 PHC 编解码；ZRT 在执行昂贵计算前额外限制数据库中可控参数，
	// 避免被篡改的摘要触发超大内存分配或长时间计算。
	if decoded.Config.Mode != argon2.ModeArgon2id || decoded.Config.Version != argon2.Version13 {
		return false, errors.New("密码摘要算法或版本无效")
	}
	if decoded.Config.MemoryCost < 8*1024 || decoded.Config.MemoryCost > 256*1024 ||
		decoded.Config.TimeCost < 1 || decoded.Config.TimeCost > 10 ||
		decoded.Config.Parallelism < 1 || decoded.Config.Parallelism > 8 {
		return false, errors.New("密码摘要参数超出安全范围")
	}
	if len(decoded.Salt) < 16 || len(decoded.Salt) > 64 {
		return false, errors.New("密码摘要盐无效")
	}
	if len(decoded.Hash) < 16 || len(decoded.Hash) > 64 {
		return false, errors.New("密码摘要内容无效")
	}
	matched, err := decoded.Verify([]byte(password))
	if err != nil {
		return false, errors.New("密码摘要格式无效")
	}
	return matched, nil
}
