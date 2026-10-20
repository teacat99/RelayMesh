package cli

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

const (
	systemdUnitName       = "relaymesh-agent.service"
	systemdHolderUnitName = "relaymesh-holder.service"
	launchdLabel          = "com.relaymesh.agent"
	launchdHolderLabel    = "com.relaymesh.holder"
)

// systemdQuote 按 systemd 的 ExecStart 规则引用一个参数：
// 含空白 / 引号 / 反斜杠等字符时加双引号并转义；`%` 与 `$` 在 systemd 里有特殊含义（说明符 / 变量展开），必须写成 `%%` 与 `$$`。
func systemdQuote(arg string) (string, error) {
	for _, r := range arg {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("参数含有控制字符，无法安全写入服务单元: %q", arg)
		}
	}
	if arg == "" {
		return `""`, nil
	}
	safe := true
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@+=:,./-", r):
		default:
			safe = false
		}
	}
	if safe {
		return arg, nil
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '%':
			b.WriteString("%%")
		case '$':
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// renderSystemdUnit 生成单进程模式（会话内嵌在 Agent 里）的 systemd 单元文件。
//
// 刻意不加任何沙箱类指令（ProtectSystem / PrivateTmp / NoNewPrivileges 等）：
// Agent 的职责就是给管理员一个完整权限的 shell，沙箱会让终端里的命令莫名失败。
// KillMode 保持默认（control-group）：服务停止时，其下的 shell 与子进程一并终止——内嵌模式下会话的寿命不超过 Agent 进程。
func renderSystemdUnit(argv []string, userScope bool) (string, error) {
	return renderSystemd(systemdSpec{
		description: "RelayMesh Terminal Agent",
		argv:        argv,
		userScope:   userScope,
		restartSec:  5,
		after:       []string{"network-online.target"},
		wants:       []string{"network-online.target"},
	})
}

// renderSystemdAgentUnit 生成与会话守护进程配套的 Agent 单元：Wants + After 守护进程单元，
// 但不是 Requires / BindsTo——守护进程重启或停止不应连带重启 Agent，反之亦然（这正是会话能挺过 Agent 重启的原因）。
func renderSystemdAgentUnit(argv []string, userScope bool) (string, error) {
	return renderSystemd(systemdSpec{
		description: "RelayMesh Terminal Agent",
		argv:        argv,
		userScope:   userScope,
		restartSec:  5,
		after:       []string{"network-online.target", systemdHolderUnitName},
		wants:       []string{"network-online.target", systemdHolderUnitName},
	})
}

// renderSystemdHolderUnit 生成会话守护进程的单元。它独立于 Agent 单元（各有自己的 cgroup），
// 重启 Agent 不会终止它的 shell；而停止它（systemctl stop）会有意终止全部会话——这是「结束会话」的明确动作。
// RestartSec 取短：崩溃后越快拉起，会话丢失的窗口越小（崩溃本身丢失的会话由下次启动如实上报）。
func renderSystemdHolderUnit(argv []string, userScope bool) (string, error) {
	return renderSystemd(systemdSpec{
		description: "RelayMesh Terminal Session Holder",
		argv:        argv,
		userScope:   userScope,
		restartSec:  2,
	})
}

type systemdSpec struct {
	description string
	argv        []string
	userScope   bool
	restartSec  int
	after       []string
	wants       []string
}

func renderSystemd(sp systemdSpec) (string, error) {
	argv := sp.argv
	if len(argv) == 0 || !strings.HasPrefix(argv[0], "/") {
		return "", fmt.Errorf("可执行文件必须是绝对路径")
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		q, err := systemdQuote(a)
		if err != nil {
			return "", err
		}
		quoted[i] = q
	}
	wantedBy := "multi-user.target"
	if sp.userScope {
		wantedBy = "default.target"
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + sp.description + "\n")
	b.WriteString("Documentation=https://github.com/teacat99/RelayMesh\n")
	if len(sp.after) > 0 {
		b.WriteString("After=" + strings.Join(sp.after, " ") + "\n")
	}
	if len(sp.wants) > 0 {
		b.WriteString("Wants=" + strings.Join(sp.wants, " ") + "\n")
	}
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + strings.Join(quoted, " ") + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString(fmt.Sprintf("RestartSec=%d\n", sp.restartSec))
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=" + wantedBy + "\n")
	return b.String(), nil
}

// renderLaunchdPlist 生成 launchd 的 plist。KeepAlive 让进程异常退出后被拉起；
// 默认的进程组终止行为保证停止服务时其下的 shell 一并结束。
func renderLaunchdPlist(argv []string, stdoutPath string) (string, error) {
	return renderLaunchdJob(launchdLabel, argv, stdoutPath)
}

// renderLaunchdHolderPlist 生成会话守护进程的 plist：与 Agent 是两个独立的 launchd 任务。
func renderLaunchdHolderPlist(argv []string, stdoutPath string) (string, error) {
	return renderLaunchdJob(launchdHolderLabel, argv, stdoutPath)
}

func renderLaunchdJob(label string, argv []string, stdoutPath string) (string, error) {
	if len(argv) == 0 || !strings.HasPrefix(argv[0], "/") {
		return "", fmt.Errorf("可执行文件必须是绝对路径")
	}
	esc := func(s string) (string, error) {
		for _, r := range s {
			if r < 0x20 && r != '\t' {
				return "", fmt.Errorf("参数含有控制字符: %q", s)
			}
		}
		var buf bytes.Buffer
		if err := xml.EscapeText(&buf, []byte(s)); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + label + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range argv {
		e, err := esc(a)
		if err != nil {
			return "", err
		}
		b.WriteString("\t\t<string>" + e + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	if stdoutPath != "" {
		e, err := esc(stdoutPath)
		if err != nil {
			return "", err
		}
		b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + e + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + e + "</string>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}
