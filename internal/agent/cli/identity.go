package cli

import (
	"errors"
	"flag"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// identityFlags 是 id / reset-id 用到的选项。
func identityFlags(name string, env Env, args []string) (dataDir string, yes bool, code int) {
	fs := newFlagSet(name, env)
	fs.StringVar(&dataDir, "data-dir", env.Getenv(envDataDir), "身份文件目录")
	if name == "reset-id" {
		fs.BoolVar(&yes, "yes", false, "不再确认，直接重置")
	}
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return "", false, 0
		}
		env.errorf("参数错误: %v\n", err)
		return "", false, 2
	}
	if dataDir == "" {
		dataDir = agent.DefaultDataDir()
	}
	return dataDir, yes, -1
}

// cmdID 只打印机器码，便于脚本使用。身份不存在时先生成（机器码从此固定）。
func cmdID(args []string, env Env) int {
	dir, _, code := identityFlags("id", env, args)
	if code >= 0 {
		return code
	}
	id, err := agent.LoadOrCreateIdentity(dir)
	if err != nil {
		env.errorf("无法加载身份: %v\n", err)
		return 1
	}
	env.printf("%s\n", id.MachineCode())
	return 0
}

// cmdResetID 重新生成身份。旧机器码仍会留在 Web 的主机列表里，需要管理员自行删除。
func cmdResetID(args []string, env Env) int {
	dir, yes, code := identityFlags("reset-id", env, args)
	if code >= 0 {
		return code
	}
	old, err := agent.LoadIdentity(dir)
	switch {
	case err == nil:
		env.printf("当前机器码: %s\n", old.MachineCode())
	case errors.Is(err, agent.ErrNoIdentity):
		env.printf("此目录下还没有身份，将直接生成新的。\n")
	default:
		env.errorf("无法读取现有身份: %v\n", err)
		return 1
	}
	if !yes && !confirm(env, "重置后机器码会改变，需要在 Web 上重新启用这台主机。继续吗？") {
		env.errorf("已取消（非交互环境请加 --yes）。\n")
		return 1
	}
	id, err := agent.ResetIdentity(dir)
	if err != nil {
		env.errorf("重置失败: %v\n", err)
		return 1
	}
	env.printf("新的机器码: %s\n", id.MachineCode())
	env.printf("若该主机正以服务方式运行，请重启服务使新身份生效；旧机器码请在 Web「终端」里删除。\n")
	return 0
}

// peekMachineCode 读取已有的机器码；不会创建身份。
func peekMachineCode(dir string) (code, path string, err error) {
	id, err := agent.LoadIdentity(dir)
	if err != nil {
		return "", agent.IdentityPath(dir), err
	}
	return id.MachineCode(), id.Path, nil
}
