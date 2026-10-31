package traceplan

// Ledger 只接收 wait/ptrace 内核事件，不能接收浏览器提供的 PID。
type Ledger struct {
	Known   map[int]bool
	Initial map[int]bool
	Pending map[int]bool
}

func New(root int) *Ledger {
	return &Ledger{Known: map[int]bool{root: true}, Initial: make(map[int]bool), Pending: make(map[int]bool)}
}

func (l *Ledger) Register(child int) (resumePending bool) {
	l.Known[child] = true
	if l.Pending[child] {
		delete(l.Pending, child)
		return true
	}
	l.Initial[child] = true
	return false
}

func (l *Ledger) Stop(tid int, initialStop, jobStop bool) string {
	if !l.Known[tid] {
		if !initialStop || len(l.Pending) >= 256 {
			return "reject"
		}
		l.Pending[tid] = true
		return "pending"
	}
	if initialStop && l.Initial[tid] {
		delete(l.Initial, tid)
		return "initial"
	}
	if jobStop {
		return "listen"
	}
	return "continue"
}

func (l *Ledger) Exec(tid, former int) bool {
	if !l.Known[tid] && !l.Known[former] {
		return false
	}
	delete(l.Known, former)
	delete(l.Initial, former)
	delete(l.Pending, former)
	l.Known[tid] = true
	return true
}

func (l *Ledger) Exit(tid int) { delete(l.Known, tid); delete(l.Initial, tid); delete(l.Pending, tid) }
