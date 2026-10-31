package traceplan

import "testing"

func TestInitialStopOrdersNeverBecomeJobControl(t *testing.T) {
	for _, childFirst := range []bool{false, true} {
		l := New(10)
		if childFirst && l.Stop(11, true, false) != "pending" {
			t.Fatal("初停未保留")
		}
		resume := l.Register(11)
		if resume != childFirst {
			t.Fatal("初停到达顺序错误")
		}
		if !childFirst && l.Stop(11, true, false) != "initial" {
			t.Fatal("初停误当作业停止")
		}
		if l.Stop(11, false, true) != "listen" {
			t.Fatal("真实作业停止未监听")
		}
		if l.Stop(11, false, false) != "continue" {
			t.Fatal("普通信号被误吞")
		}
	}
}

func TestNonLeaderExecMovesOnlyKnownIdentity(t *testing.T) {
	l := New(10)
	l.Register(11)
	if !l.Exec(10, 11) || l.Known[11] || !l.Known[10] {
		t.Fatal("exec身份没有收敛")
	}
	if l.Exec(200, 201) || l.Stop(300, false, false) != "reject" {
		t.Fatal("接受未知PID")
	}
	l.Exit(10)
	if len(l.Known) != 0 || len(l.Initial) != 0 || len(l.Pending) != 0 {
		t.Fatal("旧身份残留")
	}
}
