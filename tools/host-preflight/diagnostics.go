package main

import (
	"strconv"
	"strings"
)

// 诊断只携带固定枚举和数字退出码，绝不包含远端 stdout/stderr 原文。
type preflightQueryError struct {
	stage         string
	exitCode      int
	queryExitCode int
	reason        string
	config        *k3dConfigMetadata
}

type k3dConfigMetadata struct {
	etcReadable, outputReadable, etcSymlink, nullCharacterDevice bool
}

func (*preflightQueryError) Error() string {
	return "固定只读查询失败，未输出不完整库存"
}

func preflightExitCode(err error) int {
	code := -1
	switch value := err.(type) {
	case interface{ ExitStatus() int }:
		code = value.ExitStatus()
	case interface{ ExitCode() int }:
		code = value.ExitCode()
	}
	if code < 0 || code > 255 {
		return -1
	}
	return code
}

func readPreflightDiagnostics(buffer *limitedBuffer) (string, int) {
	stage, code := "unknown", -1
	value, exceeded := buffer.snapshot()
	if exceeded {
		return stage, code
	}
	for _, line := range strings.Split(value, "\n") {
		if current, ok := strings.CutPrefix(line, "EDO_HOST_PREFLIGHT_STAGE="); ok && validPreflightDiagnosticStage(current) {
			stage, code = current, -1
			continue
		}
		if current, ok := strings.CutPrefix(line, "EDO_HOST_PREFLIGHT_QUERY_EXIT="); ok && (strings.HasPrefix(stage, "K3D_") || stage == "k3d_config_probe") {
			if parsed, err := strconv.Atoi(current); err == nil && parsed >= 0 && parsed <= 255 && strconv.Itoa(parsed) == current {
				code = parsed
			}
		}
	}
	return stage, code
}

func validPreflightDiagnosticStage(stage string) bool {
	if stage == "baseline" || stage == "native_preparation" || stage == "k3d_identity" || stage == "k3d_config_probe" {
		return true
	}
	for _, query := range k3dInventoryQueries {
		if stage == query.section {
			return true
		}
	}
	return false
}

func readPreflightConfigMetadata(buffer *limitedBuffer) *k3dConfigMetadata {
	value, exceeded := buffer.snapshot()
	if exceeded {
		return nil
	}
	probe, seen := false, false
	var result *k3dConfigMetadata
	for _, line := range strings.Split(value, "\n") {
		if stage, ok := strings.CutPrefix(line, "EDO_HOST_PREFLIGHT_STAGE="); ok {
			probe = stage == "k3d_config_probe"
		}
		if bits, ok := strings.CutPrefix(line, "EDO_HOST_PREFLIGHT_CONFIG_META="); ok {
			if seen || !probe || len(bits) != 4 || strings.Trim(bits, "01") != "" {
				return nil
			}
			seen = true
			result = &k3dConfigMetadata{etcReadable: bits[0] == '1', outputReadable: bits[1] == '1', etcSymlink: bits[2] == '1', nullCharacterDevice: bits[3] == '1'}
		}
	}
	return result
}
