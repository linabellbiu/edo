package k3s

import (
	"log/slog"
	"strconv"
	"strings"
)

var k3dToolFailureMessages = map[string]string{
	"invalid_file": "已有 k3d 工具文件类型或执行权限不符合要求，请核对文件；不会自动覆盖",
	"checksum":     "已有 k3d 工具未通过官方完整性校验，请核对文件是否完整；不会自动覆盖",
	"version":      "已有 k3d 工具版本不符合本次安装要求，请核对工具文件",
	"dns":          "所选主机无法解析 k3d 官方下载域名，请检查主机的 DNS 后重新检测",
	"connect":      "所选主机无法连接 k3d 官方下载源，请检查主机访问 GitHub 的网络后重新检测",
	"timeout":      "访问 k3d 官方下载源超时，已重试仍未成功，请检查主机访问 GitHub 的网络后重新检测",
	"tls":          "所选主机无法与 k3d 官方下载源建立安全连接，请检查主机时间、证书和代理设置",
	"denied":       "k3d 官方下载源拒绝访问，请检查主机网络或代理后重新检测",
	"not_found":    "k3d 官方下载源未找到所需版本或架构的工具文件，请核对工具版本后重新检测",
	"rate_limit":   "k3d 官方下载源暂时限制了请求频率，请稍后重新检测",
	"http":         "k3d 官方下载源暂不可用，请稍后重新检测",
	"redirect":     "k3d 官方下载源重定向异常，请检查主机网络或代理后重新检测",
	"unavailable":  "无法完成 k3d 官方工具下载检测，请检查主机网络和下载命令后重新检测",
}

func parseK3dPreflight(host Host, output string, readErr error) HostPreflight {
	report := parseHostPreflight(host, output, readErr, k3dPreflightDefinitions)
	if readErr != nil {
		return report
	}
	// 只接收已登记的状态和数字诊断信息，远端原始错误不进入用户提示。
	var reason string
	var exitCode, httpStatus int
	found, valid := false, true
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if fields[0] != "EDO_K3S_K3D_TOOL" {
			continue
		}
		duplicate := found
		found = true
		if duplicate || len(fields) != 4 {
			valid = false
			continue
		}
		reason = fields[1]
		var exitErr, httpErr error
		exitCode, exitErr = strconv.Atoi(fields[2])
		httpStatus, httpErr = strconv.Atoi(fields[3])
		if exitErr != nil || httpErr != nil || exitCode < 0 || exitCode > 255 || httpStatus < 0 || httpStatus > 599 {
			valid = false
		}
		if reason != "ready" && k3dToolFailureMessages[reason] == "" {
			valid = false
		}
	}
	if !found {
		return report
	}
	for _, item := range report.Checks {
		if item.ID == "download" && (reason == "ready") != (item.Status == "passed") {
			valid = false
		}
	}
	if !valid {
		slog.Warn("k3d 工具检测反馈格式无效", "operation", "k3d_tool_preflight_feedback", "host_id", host.ID)
		report.Passed = false
		report.Checks = append(report.Checks, check("tool_report", "k3d 工具检测结果", "failed", "k3d 工具检测反馈不完整，请重新检测"))
		return report
	}
	for index := range report.Checks {
		item := &report.Checks[index]
		if item.ID != "download" || item.Status != "failed" {
			continue
		}
		message := k3dToolFailureMessages[reason]
		if message != "" {
			item.Message = message
		}
		slog.Warn("k3d 工具检测未通过", "operation", "k3d_tool_preflight", "host_id", host.ID, "reason", reason, "exit_code", exitCode, "http_status", httpStatus)
	}
	return report
}
