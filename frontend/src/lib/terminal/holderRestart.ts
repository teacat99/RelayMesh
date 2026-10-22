// 「重启会话守护进程」命令（holder restart）的使用要点。
//
// 界面上有几处提示会让人去主机上执行 `relaymesh-agent holder restart`，而照抄不一定能用：
//   - 命令不在 PATH 里：用户级安装放在 ~/.local/bin（登录 shell 不一定把它加进 PATH），Windows 装在 Program Files 下，
//     都要写完整路径（PowerShell 里是 & '<完整路径>' holder restart）；
//   - 系统级安装的守护进程以 root / 管理员身份运行：要加 sudo（Windows 用「以管理员身份运行」的 PowerShell），
//     否则命令会去找自己目录下的套接字，报「会话守护进程没有运行」。
// 升级 / 安装命令结束时，Agent 会按这台主机的实际情况把整条命令拼好打印出来，直接复制那一行最省事。

export const HOLDER_RESTART_TIP =
  '执行时：命令找不到就写完整路径（用户级安装在 ~/.local/bin，Windows 在 C:\\Program Files\\RelayMesh Agent\\，PowerShell 里写成 & \'<完整路径>\' holder restart）；'
  + '系统级安装的主机要在前面加 sudo（Windows 用「以管理员身份运行」的 PowerShell），否则会报「会话守护进程没有运行」。'
  + '升级命令结束时打印的那一行已经按这台主机的情况拼好，可以直接复制。'
