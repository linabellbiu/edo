//go:build linux && (amd64 || arm64)

// edo-terminal-session 是显式预装到目标镜像的受控终端工具，不是 EDO 常驻服务。
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"edo/internal/terminalsession"
	"edo/internal/terminalwire"
)

func init() {
	// launcher 必须保留 Go 启动主线程；不在共享库中改变 EDO 服务进程。
	if len(os.Args) == 3 && os.Args[1] == "launcher" {
		runtime.LockOSThread()
	}
}

func main() {
	// 只限制当前工具进程；容器原环境不变，不能把这些设置传给用户 Shell。
	runtime.GOMAXPROCS(1)
	debug.SetMemoryLimit(24 << 20)
	code := 64
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "--version":
			fmt.Printf("EDO terminal session protocol %d\n", terminalwire.Version)
			return
		case "controller":
			code = terminalsession.Controller()
		}
	} else if len(os.Args) == 3 {
		if _, ok := terminalwire.ShellPath(os.Args[2]); ok {
			switch os.Args[1] {
			case "worker":
				code = terminalsession.Worker(os.Args[2])
			case "launcher":
				code = terminalsession.Launcher(os.Args[2])
			}
		}
	}
	os.Exit(code)
}
