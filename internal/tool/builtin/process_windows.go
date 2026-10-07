//go:build windows

package builtin

import (
	"fmt"
	"os/exec"
	"syscall"
)

// startProcess 在 Windows 上隐藏控制台窗口启动。
func startProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.Start()
}

// stopProcess 在 Windows 上用 taskkill 终止整棵进程树：
// 非 force 先无 /F（先发 WM_CLOSE，子进程 /T 继承），等待后由 wait 收口；
// force 直接 taskkill /T /F。
func stopProcess(cmd *exec.Cmd, force bool) error {
	if cmd.Process == nil {
		return nil
	}
	args := []string{"/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	// taskkill 退出码非 0（进程已消失）时忽略，实际终止状态由 cmd.Wait 收口。
	_ = exec.Command("taskkill", args...).Run()
	return nil
}
