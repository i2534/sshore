//go:build !windows

package clipboard

import (
	"errors"
	"testing"
)

func TestSupportFalseOnNonWindows(t *testing.T) {
	if Support() {
		t.Error("非 Windows 平台上 Support() 必须是 false")
	}
}

func TestSetFilesUnsupportedOnNonWindows(t *testing.T) {
	err := SetFiles([]string{"/tmp"})
	if err == nil {
		t.Fatal("非 Windows 平台上 SetFiles 必须返回错误，而不是静默成功")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err=%v, want ErrUnsupported", err)
	}
}
