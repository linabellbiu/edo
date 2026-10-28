package terminalfiles

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCleanPathDoesNotInterpretShellCharacters(t *testing.T) {
	for _, invalid := range []string{"", "relative", "/null\x00name", "/bad\xff", "/a/../b"} {
		if _, err := CleanPath(invalid); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("无效路径未拒绝: %q %v", invalid, err)
		}
	}
	if p, err := CleanPath("/tmp/./' $(literal)\nfile"); err != nil || p != "/tmp/' $(literal)\nfile" {
		t.Fatalf("路径不透明: %q %v", p, err)
	}
}

func TestCopyBoundedRejectsExcessInput(t *testing.T) {
	var out bytes.Buffer
	if n, err := CopyBounded(&out, strings.NewReader("abcde"), 3); n != 4 || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("未识别超限: %d %v", n, err)
	}
}
