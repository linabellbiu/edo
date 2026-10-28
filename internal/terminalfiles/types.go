// Package terminalfiles 定义终端文件能力的有界协议，主机与容器共用权限和传输限制。
package terminalfiles

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	MaxEntries             = 1000
	MaxTextBytes     int64 = 1024 * 1024
	MaxTransferBytes int64 = 64 * 1024 * 1024
)

var (
	ErrInvalidPath = errors.New("文件路径无效")
	ErrNotRegular  = errors.New("仅支持普通文件")
	ErrTooLarge    = errors.New("文件超过当前操作的大小限制")
	ErrConflict    = errors.New("文件已变化，请重新读取后再操作")
	ErrExists      = errors.New("同名文件已存在，请确认后再覆盖")
	ErrUnavailable = errors.New("当前目标暂不支持文件操作")
)

type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Mode       uint32    `json:"mode"`
}

type Directory struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// Backend 不接受命令、凭据或删除操作。Put 必须先校验 expected，再原子替换普通文件。
// expected 为空时只允许新建；覆盖文件必须与该文件内容的 SHA256 摘要一致。
type Backend interface {
	List(context.Context, string) (Directory, error)
	Open(context.Context, string) (io.ReadCloser, Entry, error)
	Put(context.Context, string, io.Reader, int64, string) (Entry, error)
	Close() error
}
