package host

import (
	"io"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"edo/internal/terminalfiles"
)

// SDK 的 ReadDirContext 不提供数量上限；限制专用连接的目录响应字节，
// 由 SDK 继续处理协议及错误，避免大目录在内存中无限累积。
type hostSFTPReadBudget struct {
	reader    io.Reader
	abort     func()
	mu        sync.Mutex
	enabled   bool
	remaining int64
	exceeded  bool
}

func (b *hostSFTPReadBudget) start(maximum int64) {
	b.mu.Lock()
	b.enabled, b.remaining, b.exceeded = true, maximum, false
	b.mu.Unlock()
}

func (b *hostSFTPReadBudget) stop() {
	b.mu.Lock()
	b.enabled = false
	b.mu.Unlock()
}

func (b *hostSFTPReadBudget) exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

func (b *hostSFTPReadBudget) Read(buffer []byte) (int, error) {
	b.mu.Lock()
	if b.enabled {
		if b.remaining < 0 {
			b.mu.Unlock()
			return 0, terminalfiles.ErrTooLarge
		}
		if int64(len(buffer)) > b.remaining+1 {
			buffer = buffer[:b.remaining+1]
		}
	}
	b.mu.Unlock()
	n, err := b.reader.Read(buffer)
	b.mu.Lock()
	if b.enabled {
		b.remaining -= int64(n)
		if b.remaining < 0 {
			b.exceeded = true
			b.mu.Unlock()
			b.abort()
			return 0, terminalfiles.ErrTooLarge
		}
	}
	b.mu.Unlock()
	return n, err
}

func newHostSFTPClient(client *ssh.Client) (*sftp.Client, *hostSFTPReadBudget, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = session.Close()
		}
	}()
	input, err := session.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	// SSH 会话必须持续排空 stderr，不能让远端阻塞在未读取的旁路输出。
	session.Stderr = io.Discard
	if err = session.RequestSubsystem("sftp"); err != nil {
		return nil, nil, err
	}
	budget := &hostSFTPReadBudget{reader: output, abort: func() { _ = client.Close() }}
	files, err := sftp.NewClientPipe(budget, input, sftp.MaxConcurrentRequestsPerFile(4))
	if err != nil {
		return nil, nil, err
	}
	failed = false
	return files, budget, nil
}
