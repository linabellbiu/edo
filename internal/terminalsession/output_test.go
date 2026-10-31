package terminalsession

import (
	"errors"
	"io"
	"reflect"
	"testing"

	"edo/internal/terminalwire"
)

func TestControllerOutputDelayedDataCannotFollowACK(t *testing.T) {
	var frames []string
	o := controllerOutput{write: func(m terminalwire.Message) error { frames = append(frames, m.Type); return nil }}
	read, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		data := terminalwire.Message{Type: "data", Data: []byte("fixture")}
		close(read)
		<-release
		done <- o.send(data)
	}()
	<-read
	if err := o.send(terminalwire.Message{Type: "closed", Released: true, Clean: true}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames, []string{"closed"}) {
		t.Fatal("ACK 后出现迟到数据")
	}
	if err := o.send(terminalwire.Message{Type: "closed"}); !errors.Is(err, terminalwire.ErrProtocol) {
		t.Fatal("允许重复结束帧")
	}
}

func TestControllerOutputInProgressDataFinishesBeforeACK(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var frames []string
	o := controllerOutput{write: func(m terminalwire.Message) error {
		if m.Type == "data" {
			close(entered)
			<-release
		}
		frames = append(frames, m.Type)
		return nil
	}}
	dataDone, ackDone := make(chan error, 1), make(chan error, 1)
	go func() { dataDone <- o.send(terminalwire.Message{Type: "data"}) }()
	<-entered
	go func() { ackDone <- o.send(terminalwire.Message{Type: "closed"}) }()
	close(release)
	if err := <-dataDone; err != nil {
		t.Fatal(err)
	}
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames, []string{"data", "closed"}) {
		t.Fatal("结束帧越过在途数据")
	}
}

func TestControllerOutputFailedACKStillSealsStream(t *testing.T) {
	calls := 0
	o := controllerOutput{write: func(terminalwire.Message) error { calls++; return io.ErrClosedPipe }}
	if !errors.Is(o.send(terminalwire.Message{Type: "closed"}), io.ErrClosedPipe) {
		t.Fatal("丢失 ACK 写失败")
	}
	if err := o.send(terminalwire.Message{Type: "data"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("失败后继续写终端数据")
	}
}

func TestControllerOutputLateWriteFailureCannotBecomeCleanACK(t *testing.T) {
	failures := newControllerFailure()
	entered, release, latePublish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var ack terminalwire.Message
	o := controllerOutput{failures: failures, write: func(m terminalwire.Message) error {
		if m.Type == "data" {
			close(entered)
			<-release
			return io.ErrClosedPipe
		}
		ack = m
		return nil
	}}
	dataDone, finalDone, finalizing := make(chan error, 1), make(chan int, 1), make(chan struct{})
	go func() {
		err := o.send(terminalwire.Message{Type: "data", Data: []byte("fixture")})
		<-latePublish
		if err != nil {
			failures.record(75)
		}
		dataDone <- err
	}()
	<-entered
	go func() { close(finalizing); finalDone <- o.reaped(true, true, 5) }()
	<-finalizing
	close(release)
	code := <-finalDone
	close(latePublish)
	if !errors.Is(<-dataDone, io.ErrClosedPipe) {
		t.Fatal("在途写失败丢失")
	}
	if code != 75 || !ack.Released || ack.Clean || ack.Code != 75 {
		t.Fatalf("在途写失败被误报正常：exit=%d，clean=%t，code=%d", code, ack.Clean, ack.Code)
	}
}
