package terminalsession

import "strings"

// Shell 继承目标原环境；只在未设置 TERM 时补全真实 PTY 的终端类型。
func shellEnvironment(original []string) []string {
	env := append([]string(nil), original...)
	for _, value := range env {
		if strings.HasPrefix(value, "TERM=") {
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}
