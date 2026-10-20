package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Windows 的升级：与 Linux / macOS 的 upgrade 同一个语义——只替换程序、只重启 Agent 的计划任务，
// 不重写任务定义（服务端地址、数据目录等保持原样），不动会话守护进程里正在运行的会话，机器码与启用状态不变。
// 流程逻辑与平台无关（真实的系统调用由 winOps 注入），所以能在任何平台上用假的系统做单元测试。

var (
	errWinUpgradeNeedsAdmin = errors.New("这台机器上的服务是系统级安装的，升级需要管理员权限：请「以管理员身份运行」PowerShell 后重试")
	errWinNothingToUpgrade  = errors.New("没有检测到已安装的 RelayMesh Agent 计划任务，无法升级。首次接入请使用 Web「接入主机」里的安装命令（install）")
)

type winUpgradeResult struct {
	User        bool
	TaskName    string
	BinPath     string // 控制台版
	TaskBinPath string // 计划任务实际启动的无窗口版
	Started     bool   // 重新启动任务后确实看到了进程
	// HolderState：空表示没装守护进程；"kept" 表示它正在运行、没有动；"refreshed" 表示它没在运行、已换上新版本的程序。
	HolderState string
	Notes       []string
	Commands    []string
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// installedScope 找出已经装了任务的范围：--user 只看当前用户级；否则管理员先看系统级再看用户级，
// 普通用户看用户级——只发现系统级安装时明确提示需要管理员权限，而不是笼统地说「没装」。
func (w winOps) installedScope(wantUser bool, name string, sch func(args ...string) (string, error)) (winScope, error) {
	exists := func(sc winScope) bool {
		_, err := sch("/Query", "/TN", sc.taskName)
		return err == nil
	}
	userScope, userErr := w.scopeOf(true)
	if wantUser {
		if userErr != nil {
			return winScope{}, userErr
		}
		if exists(userScope) {
			return userScope, nil
		}
		return winScope{}, errWinNothingToUpgrade
	}

	sysScope, sysErr := w.scopeOf(false)
	sysInstalled := false
	if sysErr == nil {
		// 非管理员读不一定到系统任务的定义，再看一眼它的程序在不在，避免把「没权限看」误判成「没装」。
		if exists(sysScope) {
			sysInstalled = true
		} else if _, err := os.Stat(filepath.Join(sysScope.binDir, name+"w.exe")); err == nil {
			sysInstalled = true
		}
	}
	if w.elevated && sysInstalled {
		return sysScope, nil
	}
	if userErr == nil && exists(userScope) {
		return userScope, nil
	}
	if sysInstalled {
		return winScope{}, errWinUpgradeNeedsAdmin
	}
	return winScope{}, errWinNothingToUpgrade
}

// doUpgradeWindows 替换已安装的程序并重启 Agent 的计划任务。
func doUpgradeWindows(env Env, w winOps, wantUser bool) (winUpgradeResult, error) {
	var res winUpgradeResult
	sch := func(args ...string) (string, error) {
		res.Commands = append(res.Commands, "schtasks "+strings.Join(args, " "))
		return w.run("schtasks", args...)
	}
	name := installedName(env)
	scope, err := w.installedScope(wantUser, name, sch)
	if err != nil {
		return res, err
	}
	res.User, res.TaskName = scope.user, scope.taskName
	res.BinPath = filepath.Join(scope.binDir, name+".exe")
	res.TaskBinPath = filepath.Join(scope.binDir, name+"w.exe")
	holderImage := filepath.Join(scope.binDir, name+"-holderw.exe")

	for _, installed := range []string{res.BinPath, res.TaskBinPath, holderImage} {
		if samePath(w.exe, installed) {
			return res, errors.New("当前运行的就是已安装的程序本身，没有新版本可以换上：请先获取新版本的 " + name +
				".exe（例如用 Web「主机」里「升级」给出的命令），再用新版本执行 upgrade")
		}
	}

	// 先停掉旧的 Agent 实例：正在运行的可执行文件无法被替换。守护进程不在此列，它有自己的映像和任务。
	endOut, endErr := sch("/End", "/TN", scope.taskName)
	if !w.waitGone(res.TaskBinPath, 8*time.Second) {
		msg := fmt.Sprintf("旧的 Agent 进程没有退出，无法替换 %s；请在任务管理器里结束它后重试", res.TaskBinPath)
		if endErr != nil {
			msg += fmt.Sprintf("\n（停止计划任务时返回: %v %s）", endErr, strings.TrimSpace(endOut))
			if scope.user && !w.elevated {
				msg += errWindowsHighestDeleteHint
			}
		}
		return res, errors.New(msg)
	}
	cleanStaleBinaries(scope.binDir)
	if err := installBinary(w.exe, res.BinPath); err != nil {
		return res, fmt.Errorf("复制可执行文件到 %s 失败: %w", res.BinPath, err)
	}
	if err := copyAsGUI(res.BinPath, res.TaskBinPath); err != nil {
		return res, fmt.Errorf("生成无窗口版本失败: %w", err)
	}

	if _, err := os.Stat(holderImage); err == nil {
		if w.procs != nil && len(w.procs(holderImage)) > 0 {
			res.HolderState = "kept"
			iv := w.invocation()
			res.Notes = append(res.Notes, "会话守护进程正在运行，没有动它（替换它会结束其中正在运行的会话）。"+
				"想让它也换上新版本时，在合适的时机"+iv.restartAction(env, res.BinPath, ""))
		} else if err := copyAsGUI(res.BinPath, holderImage); err != nil {
			res.Notes = append(res.Notes, "会话守护进程当前没在运行，但更新它的程序失败（"+err.Error()+"）；它下次启动时仍会用旧版本。")
		} else {
			res.HolderState = "refreshed"
		}
	}

	if out, err := sch("/Run", "/TN", scope.taskName); err != nil {
		return res, fmt.Errorf("程序已经换成新版本，但启动计划任务失败: %v\n%s\n\nAgent 现在没有运行；排除问题后可重新执行升级，或手动运行该计划任务。", err, strings.TrimSpace(out))
	}
	res.Started = w.waitStarted(res.TaskBinPath)
	return res, nil
}
