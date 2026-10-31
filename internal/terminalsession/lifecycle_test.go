package terminalsession

import (
	"sync"
	"testing"
)

func TestControllerFailureCannotBeClearedByConcurrentEOF(t *testing.T) {
	f := newControllerFailure()
	f.record(0)
	var wg sync.WaitGroup
	wg.Go(func() { f.record(inputFailureCode) })
	for range 16 {
		wg.Go(func() { f.record(0) })
	}
	wg.Wait()
	m, code := reapedResult(true, true, 5, int(f.code.Load()))
	if code != 74 || m.Clean || !m.Released || m.Code != 74 {
		t.Fatal("输入过载被正常 EOF 覆盖")
	}
	m, code = reapedResult(true, true, 2, 0)
	if code != 0 || !m.Clean || !m.Released {
		t.Fatal("正常关闭结果错误")
	}
	m, code = reapedResult(true, false, 2, 0)
	if code != 76 || m.Clean || !m.Released {
		t.Fatal("异常 worker 被误报正常")
	}
}
