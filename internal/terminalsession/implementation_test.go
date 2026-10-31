package terminalsession

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// 这里只锁定阻塞等待和 EOF 自退出的源结构；Linux 内核回收仍需真实容器验收。
func TestIdleWorkerBlocksAndEOFStillExitsWholeTracer(t *testing.T) {
	b, err := os.ReadFile("worker_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "worker_linux.go", b, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocking, locked, eofExit := false, false, false
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "PTRACE_DETACH" {
			t.Error("不能 detach 放走后代")
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Wait4":
			if owner.Name == "unix" && len(call.Args) == 4 {
				v, ok := call.Args[2].(*ast.SelectorExpr)
				blocking = ok && v.Sel.Name == "WALL"
			}
		case "LockOSThread":
			locked = locked || owner.Name == "runtime"
		case "Exit":
			if owner.Name == "os" && len(call.Args) == 1 {
				v, ok := call.Args[0].(*ast.BasicLit)
				eofExit = eofExit || ok && v.Value == "0"
			}
		case "Sleep":
			t.Error("活会话不能通过轮询制造空闲 CPU 消耗")
		}
		return true
	})
	if !blocking || !locked || !eofExit || !strings.Contains(string(b), "os.Stdin.Read") || !strings.Contains(string(b), "PTRACE_O_EXITKILL") {
		t.Fatal("阻塞等待或 EOF 回收约束丢失")
	}
}

func TestHelperCoreNeverOverridesGoBudgetEnvironment(t *testing.T) {
	for _, p := range []string{"controller_linux.go", "worker_linux.go", "launcher_linux.go"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "GOMAXPROCS=") || strings.Contains(string(b), "GOMEMLIMIT=") {
			t.Fatal("工具资源预算不能改写用户环境")
		}
	}
}
