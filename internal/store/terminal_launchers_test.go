package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const launcherHost = "RM-7K2F-9XQ4-M3WD-8ABC"

func okLauncher(id, name string) TerminalLauncher {
	return TerminalLauncher{ID: id, Name: name, Host: launcherHost, Dir: "/home/user/RelayMesh", Command: "agent"}
}

func TestTerminalLauncherRoundTripUpdateAndDelete(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	got, err := st.ListTerminalLaunchers(ctx)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("从未保存过应返回空列表（不是 nil）: %+v err=%v", got, err)
	}

	list, created, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-1", "Cursor CLI"))
	if err != nil || !created || len(list) != 1 {
		t.Fatalf("新增: %+v created=%v err=%v", list, created, err)
	}
	if _, created, err = st.UpsertTerminalLauncher(ctx, okLauncher("l-2", "日志")); err != nil || !created {
		t.Fatalf("再新增一条: created=%v err=%v", created, err)
	}

	// 按编号覆盖：位置不变、不算新增；不动另一条。
	upd := okLauncher("l-1", "Cursor CLI")
	upd.Command = "agent --resume"
	list, created, err = st.UpsertTerminalLauncher(ctx, upd)
	if err != nil || created || len(list) != 2 || list[0].ID != "l-1" || list[0].Command != "agent --resume" || list[1].Name != "日志" {
		t.Fatalf("覆盖: %+v created=%v err=%v", list, created, err)
	}

	// 目录与命令都可以留空（只开一个终端 / 用主机默认目录）。
	bare := TerminalLauncher{ID: "l-3", Name: "裸终端", Host: launcherHost}
	if _, _, err = st.UpsertTerminalLauncher(ctx, bare); err != nil {
		t.Fatalf("目录与命令留空应合法: %v", err)
	}

	list, gone, err := st.DeleteTerminalLauncher(ctx, "l-2")
	if err != nil || gone.Name != "日志" || len(list) != 2 {
		t.Fatalf("删除: %+v gone=%+v err=%v", list, gone, err)
	}
	if _, _, err = st.DeleteTerminalLauncher(ctx, "l-2"); !errors.Is(err, ErrLauncherNotFound) {
		t.Fatalf("重复删除应报不存在: %v", err)
	}
	// 删除失败不应改动已有数据。
	if after, _ := st.ListTerminalLaunchers(ctx); len(after) != 2 {
		t.Fatalf("失败的删除不应改动数据: %+v", after)
	}
}

func TestTerminalLauncherNormalizesWhitespaceButKeepsCommandBody(t *testing.T) {
	st := newMemStore(t)
	l := TerminalLauncher{ID: "l-1", Name: "  Cursor  ", Host: " " + launcherHost + " ", Dir: " /srv/app ", Command: "  FOO='a  b' agent  "}
	list, _, err := st.UpsertTerminalLauncher(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if g := list[0]; g.Name != "Cursor" || g.Host != launcherHost || g.Dir != "/srv/app" || g.Command != "FOO='a  b' agent" {
		t.Fatalf("首尾空白应去掉、命令中间的空白保持原样: %+v", g)
	}

	// 名字是单行文本：连续空白、换行、制表都折叠成一个空格——这样它和终端子系统规整后的会话名一致。
	l2 := okLauncher("l-2", " Cursor \t\n  CLI  ")
	list, _, err = st.UpsertTerminalLauncher(context.Background(), l2)
	if err != nil {
		t.Fatal(err)
	}
	if list[1].Name != "Cursor CLI" {
		t.Fatalf("名字里的空白应折叠成单个空格: %q", list[1].Name)
	}
	if got := termproto.SanitizeText(list[1].Name, termproto.MaxSessionNameRunes); got != list[1].Name {
		t.Fatalf("保存的名字应与终端子系统规整后的会话名一致: %q != %q", got, list[1].Name)
	}
}

func TestTerminalLauncherValidationRejectsBadFields(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	base := okLauncher("l-1", "ok")
	cases := []struct {
		name string
		mut  func(*TerminalLauncher)
		want string
	}{
		{"编号为空", func(l *TerminalLauncher) { l.ID = "" }, "编号"},
		{"编号含空格", func(l *TerminalLauncher) { l.ID = "a b" }, "编号"},
		{"编号太长", func(l *TerminalLauncher) { l.ID = strings.Repeat("a", 41) }, "编号"},
		{"名字为空", func(l *TerminalLauncher) { l.Name = "   " }, "名字"},
		{"名字太长", func(l *TerminalLauncher) { l.Name = strings.Repeat("字", MaxLauncherNameRunes+1) }, "名字最长"},
		{"名字带 ESC", func(l *TerminalLauncher) { l.Name = "a\x1bb" }, "名字里不能有控制字符"},
		{"名字带零宽字符", func(l *TerminalLauncher) { l.Name = "a\u200bb" }, "特殊字符"},
		{"名字带方向控制符", func(l *TerminalLauncher) { l.Name = "a\u202eb" }, "特殊字符"},
		{"主机不是机器码", func(l *TerminalLauncher) { l.Host = "devhost" }, "主机"},
		{"主机为空", func(l *TerminalLauncher) { l.Host = "" }, "主机"},
		{"目录是相对路径", func(l *TerminalLauncher) { l.Dir = "projects/app" }, "完整路径"},
		{"目录带换行", func(l *TerminalLauncher) { l.Dir = "/a\n/b" }, "目录"},
		{"目录太长", func(l *TerminalLauncher) { l.Dir = "/" + strings.Repeat("d", MaxLauncherDirRunes) }, "目录最长"},
		{"命令带换行", func(l *TerminalLauncher) { l.Command = "a\nb" }, "命令只能是一行"},
		{"命令带回车", func(l *TerminalLauncher) { l.Command = "a\rb" }, "命令只能是一行"},
		{"命令带制表符", func(l *TerminalLauncher) { l.Command = "a\tb" }, "命令只能是一行"},
		{"命令带 ESC", func(l *TerminalLauncher) { l.Command = "a\x1bb" }, "命令只能是一行"},
		{"命令带 DEL", func(l *TerminalLauncher) { l.Command = "a\x7fb" }, "命令只能是一行"},
		{"命令太长", func(l *TerminalLauncher) { l.Command = strings.Repeat("x", MaxLauncherCommandRunes+1) }, "命令最长"},
	}
	for _, c := range cases {
		l := base
		c.mut(&l)
		_, _, err := st.UpsertTerminalLauncher(ctx, l)
		if !errors.Is(err, ErrInvalidLauncher) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：应被拒绝并说明「%s」，得到 %v", c.name, c.want, err)
		}
	}
	if list, _ := st.ListTerminalLaunchers(ctx); len(list) != 0 {
		t.Fatalf("被拒绝的预设不能被保存: %+v", list)
	}

	// 边界上的合法值：各种形式的完整路径、恰好等于上限的长度。
	for i, dir := range []string{"/", "~", "~/work", "/srv/应用", `C:\work`, `D:/work`, `\\nas\share`, ""} {
		l := okLauncher(fmt.Sprintf("d-%d", i), fmt.Sprintf("目录%d", i))
		l.Dir = dir
		if _, _, err := st.UpsertTerminalLauncher(ctx, l); err != nil {
			t.Fatalf("目录 %q 应合法: %v", dir, err)
		}
	}
	long := okLauncher("long", strings.Repeat("字", MaxLauncherNameRunes))
	long.Command = strings.Repeat("字", MaxLauncherCommandRunes)
	long.Dir = "/" + strings.Repeat("目", MaxLauncherDirRunes-1)
	if _, _, err := st.UpsertTerminalLauncher(ctx, long); err != nil {
		t.Fatalf("恰好等于上限（按字数）应合法: %v", err)
	}
}

func TestTerminalLauncherNamesAreUniqueCaseInsensitive(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-1", "Cursor CLI")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-2", "cursor cli")); !errors.Is(err, ErrInvalidLauncher) || !strings.Contains(err.Error(), "已经有一个叫") {
		t.Fatalf("同名（不分大小写）应被拒绝: %v", err)
	}
	// 改自己的内容、保持原名不算重名。
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-1", "Cursor CLI")); err != nil {
		t.Fatalf("保存自己、名字不变应合法: %v", err)
	}
	// 改成别人的名字不行；改成新名字行。
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-3", "Other")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-3", "CURSOR CLI")); !errors.Is(err, ErrInvalidLauncher) {
		t.Fatalf("改名成别人的名字应被拒绝: %v", err)
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-3", "Other 2")); err != nil {
		t.Fatalf("改成新名字应合法: %v", err)
	}
}

func TestTerminalLauncherLimitOnlyBlocksNewOnes(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	for i := 0; i < MaxTerminalLaunchers; i++ {
		if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher(fmt.Sprintf("l-%d", i), fmt.Sprintf("预设 %d", i))); err != nil {
			t.Fatalf("第 %d 条应能保存: %v", i, err)
		}
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-over", "超出")); !errors.Is(err, ErrLauncherLimit) {
		t.Fatalf("超过上限的新增应被拒绝: %v", err)
	}
	// 已有的照常修改与删除。
	edit := okLauncher("l-3", "预设 3")
	edit.Command = "changed"
	if _, created, err := st.UpsertTerminalLauncher(ctx, edit); err != nil || created {
		t.Fatalf("到上限后修改已有的应可行: created=%v err=%v", created, err)
	}
	if _, _, err := st.DeleteTerminalLauncher(ctx, "l-0"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-over", "超出")); err != nil {
		t.Fatalf("删掉一条后又能新增: %v", err)
	}
}

// 库里的值被手工改坏时，读出来逐条丢弃坏的，而不是把坏数据交给界面去执行。
func TestTerminalLauncherDecodeToleratesGarbage(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	put := func(v string) {
		t.Helper()
		if err := st.WithTx(ctx, func(tx *gorm.DB) error {
			return tx.Save(&model.SystemSetting{Key: TerminalLaunchersKey, Value: v}).Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("not json at all")
	if got, err := st.ListTerminalLaunchers(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("损坏的 JSON 应按空列表处理: %+v err=%v", got, err)
	}
	put(fmt.Sprintf(`{"launchers":[
		{"id":"ok-1","name":"好的","host":%q,"command":"agent"},
		{"id":"bad id","name":"坏编号","host":%q},
		{"id":"ok-1","name":"重复编号","host":%q},
		{"id":"ok-2","name":"好的","host":%q},
		{"id":"ok-3","name":"带换行","host":%q,"command":"a\nb"},
		{"id":"ok-4","name":"没主机","host":"x"},
		{"id":"ok-5","name":"另一个好的","host":%q,"dir":"/srv"}
	]}`, launcherHost, launcherHost, launcherHost, launcherHost, launcherHost, launcherHost))
	got, err := st.ListTerminalLaunchers(ctx)
	if err != nil || len(got) != 2 || got[0].ID != "ok-1" || got[1].ID != "ok-5" {
		t.Fatalf("应只留下合法、不重复的两条: %+v err=%v", got, err)
	}

	// 数量超出上限的存档只留前 MaxTerminalLaunchers 条。
	var sb strings.Builder
	sb.WriteString(`{"launchers":[`)
	for i := 0; i < MaxTerminalLaunchers+5; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":"n-%d","name":"预设 %d","host":%q}`, i, i, launcherHost)
	}
	sb.WriteString(`]}`)
	put(sb.String())
	if got, _ := st.ListTerminalLaunchers(ctx); len(got) != MaxTerminalLaunchers {
		t.Fatalf("超出上限的存档应截断到 %d 条，得到 %d", MaxTerminalLaunchers, len(got))
	}
}

// 预设单独一个键：保存预设不能动终端偏好，反过来也一样。
func TestTerminalLaunchersDoNotTouchTerminalPrefs(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 120 }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertTerminalLauncher(ctx, okLauncher("l-1", "Cursor CLI")); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetTerminalPrefs(ctx); p.FixedCols != 120 {
		t.Fatalf("保存预设不应改动终端偏好: %+v", p)
	}
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 0 }); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListTerminalLaunchers(ctx); len(list) != 1 {
		t.Fatalf("改终端偏好不应动预设: %+v", list)
	}
}
