package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func sshHostRec(code string) *model.TerminalHost {
	return &model.TerminalHost{
		MachineCode: code, Kind: model.HostKindSSH, Status: "enabled",
		SSHAddr: "10.0.0.1:22", SSHUser: "ops", SSHAuth: "password", SSHSecret: "v1:sealed",
		SSHHostKeyType: "ssh-ed25519", SSHHostKey: "AAAA", SSHFingerprint: "SHA256:abc",
	}
}

func TestCreateSSHHostValidatesAndLimits(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	if err := st.CreateSSHHost(ctx, &model.TerminalHost{MachineCode: "RM-AAAA-AAAA-AAAA-AAAA"}); err == nil {
		t.Fatal("非 SSH 记录必须被拒绝")
	}
	if err := st.CreateSSHHost(ctx, nil); err == nil {
		t.Fatal("nil 记录必须被拒绝")
	}

	rec := sshHostRec("RM-0000-0000-0000-0001")
	if err := st.CreateSSHHost(ctx, rec); err != nil {
		t.Fatalf("创建: %v", err)
	}
	if err := st.CreateSSHHost(ctx, sshHostRec("RM-0000-0000-0000-0001")); !errors.Is(err, ErrTerminalHostExists) {
		t.Fatalf("机器码重复应返回 ErrTerminalHostExists，得到 %v", err)
	}

	for i := 2; i <= MaxSSHHosts; i++ {
		if err := st.CreateSSHHost(ctx, sshHostRec(fmt.Sprintf("RM-0000-0000-0000-%04d", i))); err != nil {
			t.Fatalf("创建第 %d 台: %v", i, err)
		}
	}
	if err := st.CreateSSHHost(ctx, sshHostRec("RM-9999-9999-9999-9999")); !errors.Is(err, ErrSSHHostLimit) {
		t.Fatalf("超出上限应返回 ErrSSHHostLimit，得到 %v", err)
	}

	// Agent 主机不占 SSH 名额。
	if err := st.SaveTerminalHost(ctx, &model.TerminalHost{MachineCode: "RM-AGNT-AGNT-AGNT-AGNT", Status: "enabled", PublicKey: "k"}); err != nil {
		t.Fatalf("保存 Agent 主机: %v", err)
	}
}

func TestUpdateSSHConnPartial(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	code := "RM-1111-1111-1111-1111"
	if err := st.CreateSSHHost(ctx, sshHostRec(code)); err != nil {
		t.Fatal(err)
	}

	addr, user := "192.168.1.9:2222", "deploy"
	if err := st.UpdateSSHConn(ctx, code, SSHConnUpdate{Addr: &addr, User: &user}); err != nil {
		t.Fatalf("更新: %v", err)
	}
	got, _ := st.GetTerminalHost(ctx, code)
	if got.SSHAddr != addr || got.SSHUser != user {
		t.Fatalf("地址或用户没有更新: %+v", got)
	}
	if got.SSHSecret != "v1:sealed" || got.SSHFingerprint != "SHA256:abc" || got.SSHAuth != "password" {
		t.Fatalf("没有指定的字段不应被修改: %+v", got)
	}

	empty := ""
	fp := "SHA256:new"
	if err := st.UpdateSSHConn(ctx, code, SSHConnUpdate{Fingerprint: &fp, KeyState: &empty, Error: &empty}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalHost(ctx, code)
	if got.SSHFingerprint != fp || got.SSHAddr != addr {
		t.Fatalf("部分更新结果不对: %+v", got)
	}

	if err := st.UpdateSSHConn(ctx, "RM-2222-2222-2222-2222", SSHConnUpdate{Addr: &addr}); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("不存在的主机应返回 ErrTerminalHostNotFound，得到 %v", err)
	}

	// 不是 SSH 主机的记录不能通过这个接口修改。
	agent := "RM-AGNT-AGNT-AGNT-AGNT"
	if err := st.SaveTerminalHost(ctx, &model.TerminalHost{MachineCode: agent, Status: "enabled", PublicKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSSHConn(ctx, agent, SSHConnUpdate{Addr: &addr}); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("Agent 主机不应被 UpdateSSHConn 修改，得到 %v", err)
	}
}

func TestRecordSSHCheckStates(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	code := "RM-3333-3333-3333-3333"
	if err := st.CreateSSHHost(ctx, sshHostRec(code)); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Truncate(time.Second)
	long := strings.Repeat("错", 500)
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, Err: long}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetTerminalHost(ctx, code)
	if got.SSHError == "" || len([]rune(got.SSHError)) != 200 {
		t.Fatalf("错误文本应被截断到 200 个字符，得到 %d", len([]rune(got.SSHError)))
	}
	if got.SSHCheckedAt == nil || got.SSHOKAt != nil {
		t.Fatalf("失败不应更新 ok_at: %+v", got)
	}

	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, Err: "密钥不一致", KeyMismatch: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalHost(ctx, code)
	if got.SSHKeyState != model.SSHKeyMismatch {
		t.Fatalf("应置位 mismatch: %q", got.SSHKeyState)
	}

	// 成功只清除错误并刷新自报信息，不会清掉 mismatch（只有管理员重新确认指纹才能清除）。
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now.Add(time.Minute), OK: true, Hostname: "box", OS: "Linux", Arch: "x86_64"}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalHost(ctx, code)
	if got.SSHError != "" || got.SSHOKAt == nil || got.Hostname != "box" || got.OS != "Linux" || got.Arch != "x86_64" {
		t.Fatalf("成功结果不对: %+v", got)
	}
	if got.SSHKeyState != model.SSHKeyMismatch {
		t.Fatal("成功的连接测试不能自动清除 mismatch")
	}

	if err := st.RecordSSHCheck(ctx, "RM-4444-4444-4444-4444", SSHCheck{OK: true}); !errors.Is(err, ErrTerminalHostNotFound) {
		t.Fatalf("不存在的主机应返回 ErrTerminalHostNotFound，得到 %v", err)
	}
}

// 「只能当人工终端用」标记只在成功的检查里刷新（失败的检查什么也不说明），也可以随连接信息一起设置。
func TestSSHTermOnlyFlagPersistence(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	code := "RM-5555-5555-5555-5555"
	if err := st.CreateSSHHost(ctx, sshHostRec(code)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); got.SSHTermOnly {
		t.Fatal("默认应可供模型执行（term_only=false）")
	}
	on, off := true, false
	now := time.Now()

	// 失败的检查不改标记，哪怕带了值。
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, Err: "unreachable", TermOnly: &on}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); got.SSHTermOnly {
		t.Fatal("失败的检查不应刷新 term_only")
	}
	// 成功的检查刷新它；没带值时保持不变。
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, OK: true, TermOnly: &on}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); !got.SSHTermOnly {
		t.Fatal("成功的检查应把 term_only 置位")
	}
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, OK: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); !got.SSHTermOnly {
		t.Fatal("没带值的检查不应改动 term_only")
	}
	if err := st.RecordSSHCheck(ctx, code, SSHCheck{At: now, OK: true, TermOnly: &off}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); got.SSHTermOnly {
		t.Fatal("目标被重装成 POSIX 系统后应能清除 term_only")
	}
	// 随连接信息一起设置。
	if err := st.UpdateSSHConn(ctx, code, SSHConnUpdate{TermOnly: &on}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalHost(ctx, code); !got.SSHTermOnly {
		t.Fatal("UpdateSSHConn 应能设置 term_only")
	}
}

// 网关据对账名单回收凭据，所以名单必须完整：不分状态、只含经该网关接入的 SSH 主机、按机器码排序。
func TestListSSHHostCodesViaGateway(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	gwA, gwB := "RM-AAAA-0000-0000-0001", "RM-BBBB-0000-0000-0002"

	mk := func(code, gateway, status string) {
		t.Helper()
		rec := sshHostRec(code)
		rec.SSHGateway, rec.Status = gateway, status
		if err := st.CreateSSHHost(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("RM-0000-0000-0000-0003", gwA, "enabled")
	mk("RM-0000-0000-0000-0001", gwA, "disabled") // 停用的也要在名单里
	mk("RM-0000-0000-0000-0002", gwA, "enabled")
	mk("RM-0000-0000-0000-0004", gwB, "enabled")
	mk("RM-0000-0000-0000-0005", "", "enabled") // 直连主机不属于任何网关
	// 一台装了 Agent 的普通主机不会出现在名单里，哪怕它的 ssh_gateway 列碰巧相同。
	if err := st.SaveTerminalHost(ctx, &model.TerminalHost{MachineCode: "RM-AGNT-AGNT-AGNT-AGNT", Status: "enabled", PublicKey: "k", SSHGateway: gwA}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListSSHHostCodesViaGateway(ctx, gwA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"RM-0000-0000-0000-0001", "RM-0000-0000-0000-0002", "RM-0000-0000-0000-0003"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("网关 A 的名单 = %v，期望 %v", got, want)
	}
	if got, _ := st.ListSSHHostCodesViaGateway(ctx, gwB); fmt.Sprint(got) != fmt.Sprint([]string{"RM-0000-0000-0000-0004"}) {
		t.Fatalf("网关 B 的名单 = %v", got)
	}
	if got, err := st.ListSSHHostCodesViaGateway(ctx, "RM-CCCC-0000-0000-0003"); err != nil || len(got) != 0 {
		t.Fatalf("没有主机的网关应得到空名单: %v %v", got, err)
	}
	// 与删除网关前的计数保持一致。
	if n, _ := st.CountSSHHostsViaGateway(ctx, gwA); n != 3 {
		t.Fatalf("计数 = %d", n)
	}
}
