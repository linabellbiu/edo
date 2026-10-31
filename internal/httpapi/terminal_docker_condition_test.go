package httpapi

import (
	"errors"
	"testing"

	terminalservice "edo/internal/terminal"
)

func TestTerminalDockerConditionMessageNeverExposesInternalError(t *testing.T) {
	private := errors.New("private infrastructure fixture")
	if got := terminalOpenErrorMessage(errors.Join(terminalservice.ErrDockerTerminalUnavailable, private)); got != terminalservice.ErrDockerTerminalUnavailable.Error() {
		t.Fatal("未返回固定运行条件说明")
	}
	if got := terminalOpenErrorMessage(private); got != "终端连接失败，请检查目标状态或联系管理员" {
		t.Fatal("内部错误泄露到界面")
	}
}
