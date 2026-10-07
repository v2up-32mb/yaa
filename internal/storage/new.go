package storage

import (
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
)

// New 根据配置创建根 Storage：type 为 sqlite 或 memory；未知类型返回错误。
func New(cfg config.StorageConfig) (Storage, error) {
	switch cfg.Type {
	case "sqlite":
		return NewSQLite(cfg)
	case "memory":
		return NewMemory(nil)
	case "":
		return nil, fmt.Errorf("storage: type is empty")
	default:
		return nil, fmt.Errorf("storage: unsupported type %q", cfg.Type)
	}
}
