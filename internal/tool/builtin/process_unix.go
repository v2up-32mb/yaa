//go:build !windows

package builtin

import (
	"os/exec"
	"syscall"
	"time"
)

// startProcess 在 Unix 上以独立进程组启动，便于按组终止整棵进程树。
func startProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// stopProcess 对进程组先优雅 SIGTERM，5s 宽限期后升级 SIGKILL；force 直接 SIGKILL。
func stopProcess(cmd *exec.Cmd, force bool) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	// 进程可能已退出，ESRCH 忽略（best-effort）。
	_ = syscall.Kill(-pgid, sig)
	if !force {
		time.Sleep(5 * time.Second)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return nil
}
