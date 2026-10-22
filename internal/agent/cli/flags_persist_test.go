package cli

import (
	"strings"
	"testing"
)

func settleWith(t *testing.T, env Env, args ...string) (common, error) {
	t.Helper()
	var c common
	fs := newFlagSet("run", env)
	c.bind(fs, env)
	if err := parse(fs, args); err != nil {
		return c, err
	}
	return c, c.settle(fs)
}

func TestPersistFlagSemantics(t *testing.T) {
	env := testEnv(t)
	tempEnv := env
	tempEnv.Getenv = func(k string) string {
		if k == envTemp {
			return "1"
		}
		return ""
	}

	t.Run("默认就是持久身份", func(t *testing.T) {
		c, err := settleWith(t, env, "--server", "s")
		if err != nil || c.temp {
			t.Fatalf("temp=%v err=%v", c.temp, err)
		}
	})
	t.Run("--temp 单独使用", func(t *testing.T) {
		c, err := settleWith(t, env, "--temp", "--server", "s")
		if err != nil || !c.temp {
			t.Fatalf("temp=%v err=%v", c.temp, err)
		}
	})
	t.Run("--persist 显式声明持久", func(t *testing.T) {
		c, err := settleWith(t, env, "--persist", "--server", "s")
		if err != nil || c.temp {
			t.Fatalf("temp=%v err=%v", c.temp, err)
		}
	})
	t.Run("命令行同时给出 --persist 与 --temp 会被拒绝", func(t *testing.T) {
		for _, args := range [][]string{{"--persist", "--temp"}, {"--temp", "--persist"}} {
			if _, err := settleWith(t, env, args...); err == nil || !strings.Contains(err.Error(), "互斥") {
				t.Fatalf("%v: 应报互斥，得到 %v", args, err)
			}
		}
	})
	t.Run("环境变量要求临时时，命令行 --persist 说了算", func(t *testing.T) {
		c, err := settleWith(t, tempEnv, "--server", "s")
		if err != nil || !c.temp {
			t.Fatalf("仅环境变量: temp=%v err=%v", c.temp, err)
		}
		c, err = settleWith(t, tempEnv, "--persist", "--server", "s")
		if err != nil || c.temp {
			t.Fatalf("--persist 应压过环境变量: temp=%v err=%v", c.temp, err)
		}
	})
	t.Run("run 整体：互斥时退出码为 2", func(t *testing.T) {
		e := testEnv(t)
		if code := Run(t.Context(), []string{"run", "--persist", "--temp", "--server", "https://relay.example.com"}, e); code != 2 {
			t.Fatalf("code=%d", code)
		}
		if out := e.Stderr.(*syncBuffer).String(); !strings.Contains(out, "互斥") {
			t.Fatalf("应提示互斥:\n%s", out)
		}
	})
}
