package terminalwire

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestVersionedProtocolPreservesBinaryAndRejectsUnboundedInput(t *testing.T) {
	want := []byte{0, '\r', '\n', 27, 255}
	var b bytes.Buffer
	if err := Write(&b, Message{Type: "data", Data: want}); err != nil {
		t.Fatal(err)
	}
	m, err := Read(bufio.NewReaderSize(&b, MaxLine))
	if err != nil || !ValidControl(m) || !bytes.Equal(m.Data, want) {
		t.Fatal("协议改变二进制输入", err)
	}
	for _, raw := range []string{`{"version":1,"type":"data","pid":1}` + "\n", `{"version":2,"type":"close"}` + "\n", `{} {}` + "\n", strings.Repeat("x", MaxLine+1) + "\n"} {
		if _, err := Read(bufio.NewReaderSize(strings.NewReader(raw), MaxLine)); err == nil {
			t.Fatal("接受越界或未知协议")
		}
	}
}

func TestOnlyWhitelistedShellAndBoundedServerDurationAreAccepted(t *testing.T) {
	m := Message{Type: "start", Version: Version, Shell: "sh", Rows: 30, Cols: 100, MaxMS: MaxDurationMS, LeaseMS: LeaseMS}
	if !ValidStart(m) {
		t.Fatal("拒绝合法会话")
	}
	for _, change := range []func(*Message){func(x *Message) { x.Shell = "sh -c id" }, func(x *Message) { x.MaxMS++ }, func(x *Message) { x.LeaseMS++ }, func(x *Message) { x.Data = []byte("exit") }, func(x *Message) { x.Released = true }} {
		v := m
		change(&v)
		if ValidStart(v) {
			t.Fatal("接受非法启动参数")
		}
	}
	for _, shell := range []string{"sh", "bash", "ash"} {
		if _, ok := ShellPath(shell); !ok {
			t.Fatal(shell)
		}
	}
}

func TestControlAndOutputDirectionsAreSeparate(t *testing.T) {
	for _, typ := range []string{"ready", "closed", "start", "unknown"} {
		if ValidControl(Message{Type: typ, Version: Version}) {
			t.Fatal("控制帧方向错误")
		}
	}
	for _, m := range []Message{{Type: "ready", Version: Version, NativeABI: true}, {Type: "closed", Version: Version, Released: true, Clean: true, Tracked: 5}, {Type: "closed", Version: Version, Released: false, Code: 78}} {
		if !ValidOutput(m) {
			t.Fatal("拒绝合法输出")
		}
	}
	if ValidOutput(Message{Type: "closed", Version: Version, Clean: true}) || ValidOutput(Message{Type: "data", Version: Version, Data: make([]byte, MaxData+1)}) {
		t.Fatal("接受不可信输出")
	}
}
