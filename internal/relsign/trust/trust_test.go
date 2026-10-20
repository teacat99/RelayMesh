package trust

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
)

// 发布的信任根：主钥与备用钥的标识钉在这里。改名单（轮换、增删）必须同时改这个测试——
// 让信任根的变化出现在评审里，而不是悄悄夹在别的改动中。
var (
	wantPrimaryID = "90c99847aa98789c"
	wantStandbyID = "05f40badb9475e42"
)

func TestReleaseKeysAreWellFormedAndPinned(t *testing.T) {
	if len(releaseKeyTexts) == 0 || len(releaseKeyTexts) > MaxKeys {
		t.Fatalf("编进程序的公钥有 %d 把，应在 1..%d 之间", len(releaseKeyTexts), MaxKeys)
	}
	seen := map[string]bool{}
	for i, text := range releaseKeyTexts {
		if !strings.HasPrefix(text, relsign.PublicKeyScheme+":") {
			t.Errorf("releaseKeyTexts[%d] 应以 %s: 开头", i, relsign.PublicKeyScheme)
		}
		k, err := relsign.ParsePublicKey(text)
		if err != nil {
			t.Errorf("releaseKeyTexts[%d] 无法解析: %v", i, err)
			continue
		}
		if text != relsign.FormatPublicKey(k) {
			t.Errorf("releaseKeyTexts[%d] 不是规范写法（rmpub1: + 小写十六进制）", i)
		}
		id := relsign.KeyID(k)
		if seen[id] {
			t.Errorf("releaseKeyTexts[%d] 与前面的公钥重复（标识 %s）", i, id)
		}
		seen[id] = true
	}
	ids := IDs()
	if len(ids) != len(releaseKeyTexts) {
		t.Fatalf("解析出 %d 把，编进去的有 %d 把", len(ids), len(releaseKeyTexts))
	}
	if ids[0] != wantPrimaryID {
		t.Errorf("主钥标识 %s，期望 %s（改信任根要同时改这个测试）", ids[0], wantPrimaryID)
	}
	if len(ids) < 2 || ids[1] != wantStandbyID {
		t.Errorf("备用钥标识 %v，期望 %s（改信任根要同时改这个测试）", ids, wantStandbyID)
	}
}

func TestOverride(t *testing.T) {
	good := releaseKeyTexts[0]
	other := releaseKeyTexts[1]
	prev := override
	t.Cleanup(func() { override = prev })

	for name, tc := range map[string]struct {
		set  string
		want []string
	}{
		"空 = 编进去的名单":    {"", releaseKeyTexts},
		"只有空白 = 编进去的名单": {"  \t", releaseKeyTexts},
		"none = 空名单":    {"none", nil},
		"none 两侧的空白被忽略": {" none ", nil},
		"换成一把":          {good, []string{good}},
		"换成两把，空条目被忽略":   {" " + good + " ,, " + other + ",", []string{good, other}},
	} {
		override = tc.set
		got := Texts()
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: 得到 %v，期望 %v", name, got, tc.want)
		}
	}

	// 写错的条目不会让解析崩溃，只是不进入可信名单。
	override = good + ",rmpub1:zz,not-a-key"
	if got := len(Keys()); got != 1 {
		t.Errorf("写错的条目应被跳过，可信的公钥应只有 1 把，得到 %d 把", got)
	}
}

func TestTextsReturnsACopy(t *testing.T) {
	a := Texts()
	a[0] = "changed"
	if Texts()[0] == "changed" {
		t.Fatal("Texts 应返回副本：调用方改它不能改动编进去的名单")
	}
}

func TestSetTextsForTestRestores(t *testing.T) {
	before := IDs()
	restore := SetTextsForTest(nil)
	if got := Keys(); len(got) != 0 {
		t.Fatalf("替换成空名单后应没有公钥，得到 %d 把", len(got))
	}
	restore()
	if got := IDs(); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("恢复之后名单应回到 %v，得到 %v", before, got)
	}

	// 嵌套替换：内层恢复回外层的值。
	outer := SetTextsForTest([]string{releaseKeyTexts[1]})
	inner := SetTextsForTest(nil)
	if len(Keys()) != 0 {
		t.Fatal("内层应是空名单")
	}
	inner()
	if ids := IDs(); len(ids) != 1 || ids[0] != wantStandbyID {
		t.Fatalf("内层恢复后应回到外层的名单（只有备用钥），得到 %v", ids)
	}
	outer()
	if got := IDs(); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("全部恢复后名单应回到 %v，得到 %v", before, got)
	}
}

// 信任根只属于 Agent 侧代码与发布工具：Hub 服务端（internal/terminal、internal/api……）一旦 import 它，
// 就有可能被写成「Hub 自己验签、自己说了算」——那正是静默升级要避免的单点。新增使用方必须来改这个白名单，并说明理由。
func TestOnlyAgentSideCodeImportsTheTrustRoot(t *testing.T) {
	const importPath = "github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
	allowed := []string{
		"internal/relsign/trust", // 包自己
		"internal/agent",         // Agent（含 cli、测试）：验签与可信名单
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{".git": true, "node_modules": true, "frontend": true, "web": true, "data": true, "bin": true, ".cursor": true, ".agents": true, "certs": true}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // 语法错误由编译去报
		}
		checked++
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p != importPath {
				continue
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			rel = filepath.ToSlash(rel)
			ok := false
			for _, a := range allowed {
				if rel == a || strings.HasPrefix(rel, a+"/") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s import 了信任根（%s）：只有 %v 可以——Hub 服务端不该持有验签的信任根", filepath.ToSlash(path), importPath, allowed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("只扫描到 %d 个 Go 文件，仓库根路径推断有误（%s）", checked, root)
	}
}
