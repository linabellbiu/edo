//go:build darwin

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "受控 Docker 终端工具只在 Linux amd64/arm64 目标容器中运行")
	os.Exit(64)
}
