// Package terminalwire 定义 Docker 受控终端的有界内部协议，不接受命令或 PID。
package terminalwire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	Version       = 1
	HelperPath    = "/usr/local/libexec/edo-terminal-session"
	MaxLine       = 64 * 1024
	MaxData       = 32 * 1024
	MaxDurationMS = 24 * 60 * 60 * 1000
	LeaseMS       = 15000
)

var ErrProtocol = errors.New("受控终端协议无效")

// Data 仅在内存中传递，不得写入日志、审计或持久存储。
type Message struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	Shell     string `json:"shell,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	MaxMS     int64  `json:"max_ms,omitempty"`
	LeaseMS   int    `json:"lease_ms,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Released  bool   `json:"released,omitempty"`
	Clean     bool   `json:"clean,omitempty"`
	Code      int    `json:"code,omitempty"`
	Tracked   int    `json:"tracked,omitempty"`
	NativeABI bool   `json:"native_abi,omitempty"`
}

func Decode(b []byte) (Message, error) {
	var m Message
	if len(b) > MaxLine || len(b) == 0 || b[len(b)-1] != '\n' {
		return m, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != Version || len(m.Data) > MaxData {
		return Message{}, ErrProtocol
	}
	return m, nil
}

func Read(r *bufio.Reader) (Message, error) {
	b, err := r.ReadSlice('\n')
	if err == io.EOF && len(b) == 0 {
		return Message{}, io.EOF
	}
	if err != nil {
		return Message{}, ErrProtocol
	}
	return Decode(b)
}

func Write(w io.Writer, m Message) error { m.Version = Version; return json.NewEncoder(w).Encode(m) }

func ShellPath(shell string) (string, bool) {
	switch shell {
	case "sh":
		return "/bin/sh", true
	case "bash":
		return "/bin/bash", true
	case "ash":
		return "/bin/ash", true
	}
	return "", false
}

func validSize(m Message) bool { return m.Rows >= 5 && m.Rows <= 200 && m.Cols >= 20 && m.Cols <= 500 }
func noResult(m Message) bool {
	return !m.Released && !m.Clean && m.Code == 0 && m.Tracked == 0 && !m.NativeABI
}
func noStart(m Message) bool { return m.Shell == "" && m.MaxMS == 0 && m.LeaseMS == 0 }

func ValidStart(m Message) bool {
	_, shell := ShellPath(m.Shell)
	return m.Version == Version && m.Type == "start" && shell && validSize(m) && m.MaxMS >= 1000 && m.MaxMS <= MaxDurationMS && m.LeaseMS >= 1000 && m.LeaseMS <= LeaseMS && len(m.Data) == 0 && noResult(m)
}

func ValidControl(m Message) bool {
	if m.Version != Version || !noStart(m) || !noResult(m) {
		return false
	}
	switch m.Type {
	case "data":
		return m.Rows == 0 && m.Cols == 0 && len(m.Data) > 0 && len(m.Data) <= MaxData
	case "resize":
		return validSize(m) && len(m.Data) == 0
	case "ping", "close":
		return m.Rows == 0 && m.Cols == 0 && len(m.Data) == 0
	}
	return false
}

func ValidOutput(m Message) bool {
	if m.Version != Version || !noStart(m) || m.Rows != 0 || m.Cols != 0 {
		return false
	}
	switch m.Type {
	case "ready":
		return m.NativeABI && !m.Released && !m.Clean && m.Code == 0 && m.Tracked == 0 && len(m.Data) == 0
	case "data":
		return noResult(m) && len(m.Data) > 0 && len(m.Data) <= MaxData
	case "closed":
		return !m.NativeABI && len(m.Data) == 0 && m.Code >= 0 && m.Code <= 255 && m.Tracked >= 0 && m.Tracked <= 512 && (!m.Clean || m.Released && m.Code == 0)
	}
	return false
}
