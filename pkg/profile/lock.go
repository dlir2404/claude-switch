package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"claude-switch/pkg/config"
)

type Lock struct {
	file *os.File
}

func AcquireSwitchLock() (*Lock, error) {
	baseDir := config.GetBaseDir()
	_ = os.MkdirAll(baseDir, 0700)
	lockPath := filepath.Join(baseDir, "switch.lock")

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("không thể mở file khóa: %w", err)
	}

	// Non-blocking flock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("một tiến trình chuyển đổi tài khoản khác đang chạy (lock held)")
	}

	return &Lock{file: f}, nil
}

func (l *Lock) Release() {
	if l != nil && l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
}
