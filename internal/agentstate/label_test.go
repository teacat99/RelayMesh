package agentstate

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestDirectoryName(t *testing.T) {
	cases := map[string]string{
		"/home/user/RelayMesh":        "RelayMesh",
		"/home/user/RelayMesh/":       "RelayMesh",
		`C:\Users\me\code\RelayMesh`:  "RelayMesh",
		`C:\Users\me\code\RelayMesh\`: "RelayMesh",
		"  /a/b  ":                    "b",
		"relative":                    "relative",
		"/":                           "",
		"":                            "",
		"   ":                         "",
	}
	for in, want := range cases {
		if got := DirectoryName(in); got != want {
			t.Errorf("DirectoryName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelsUniqueNamesUseDirectoryName(t *testing.T) {
	out := Labels([]LabelInput{
		{ID: 1, HostName: "devhost", ProjectDirectory: "/home/user/RelayMesh"},
		{ID: 2, HostName: "devhost", ProjectDirectory: "/home/user/Other"},
	})
	if out[1] != (Label{Text: "RelayMesh"}) || out[2] != (Label{Text: "Other"}) {
		t.Fatalf("labels = %+v", out)
	}
}

func TestLabelsRenameWins(t *testing.T) {
	out := Labels([]LabelInput{{ID: 1, ProjectDirectory: "/a/b", Name: "  前端助手 "}})
	if out[1].Text != "前端助手" || out[1].Degraded {
		t.Fatalf("label = %+v", out[1])
	}
}

func TestLabelsDuplicatesDegradeToHostAndFullPath(t *testing.T) {
	out := Labels([]LabelInput{
		{ID: 1, HostName: "devhost", ProjectDirectory: "/home/user/app"},
		{ID: 2, HostName: "laptop", ProjectDirectory: "/Users/me/app"},
		{ID: 3, HostName: "devhost", ProjectDirectory: "/home/user/other"},
	})
	if out[1] != (Label{Text: "devhost:/home/user/app", Degraded: true}) {
		t.Fatalf("label 1 = %+v", out[1])
	}
	if out[2] != (Label{Text: "laptop:/Users/me/app", Degraded: true}) {
		t.Fatalf("label 2 = %+v", out[2])
	}
	if out[3] != (Label{Text: "other"}) {
		t.Fatalf("label 3 = %+v", out[3])
	}
}

// 重名比较不分大小写；改名与别人的目录名撞了，两边都降级。
func TestLabelsDuplicateIsCaseInsensitiveAndIncludesRenames(t *testing.T) {
	out := Labels([]LabelInput{
		{ID: 1, HostName: "h", ProjectDirectory: "/x/App"},
		{ID: 2, HostName: "h", ProjectDirectory: "/y/zzz", Name: "app"},
	})
	if !out[1].Degraded || !out[2].Degraded {
		t.Fatalf("labels = %+v", out)
	}
	if out[1].Text != "h:/x/App" || out[2].Text != "h:/y/zzz" {
		t.Fatalf("labels = %+v", out)
	}
}

func TestLabelsSameHostSamePathUnderTwoCredentialsStillDistinct(t *testing.T) {
	out := Labels([]LabelInput{
		{ID: 7, HostName: "h", ProjectDirectory: "/x/app"},
		{ID: 9, HostName: "h", ProjectDirectory: "/x/app"},
	})
	if out[7].Text == out[9].Text {
		t.Fatalf("labels collide: %+v", out)
	}
	if out[7].Text != "h:/x/app #7" || out[9].Text != "h:/x/app #9" {
		t.Fatalf("labels = %+v", out)
	}
}

func TestLabelsEmptyDirectoryAndMissingHost(t *testing.T) {
	out := Labels([]LabelInput{{ID: 1}})
	if out[1].Text != "未知项目" {
		t.Fatalf("label = %+v", out[1])
	}
	dup := Labels([]LabelInput{{ID: 1}, {ID: 2, HostName: "h"}})
	if dup[1].Text == dup[2].Text {
		t.Fatalf("two unknown projects collide: %+v", dup)
	}
	noHost := Labels([]LabelInput{
		{ID: 1, ProjectDirectory: "/a/app"},
		{ID: 2, ProjectDirectory: "/b/app"},
	})
	if noHost[1].Text != "/a/app" || noHost[2].Text != "/b/app" {
		t.Fatalf("labels without host = %+v", noHost)
	}
}

// TestLabelsGolden 与前端 src/lib/identity.test.ts 读同一个对照表：显示名的算法在 Go 与 TypeScript 里各有一份，
// 任何一边单独改了规则，另一边的测试就会失败，两处不会悄悄分叉。
func TestLabelsGolden(t *testing.T) {
	const path = "../../frontend/src/lib/identityLabels.golden.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("没有前端源码（%s），跳过对照表", path)
		}
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name  string `json:"name"`
			Items []struct {
				ID               uint   `json:"id"`
				HostName         string `json:"host_name"`
				ProjectDirectory string `json:"project_directory"`
				Name             string `json:"name"`
			} `json:"items"`
			Want map[string]struct {
				Text     string `json:"text"`
				Degraded bool   `json:"degraded"`
			} `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 10 {
		t.Fatalf("对照表只有 %d 个用例，文件被截断了？", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		inputs := make([]LabelInput, 0, len(c.Items))
		for _, it := range c.Items {
			inputs = append(inputs, LabelInput{ID: it.ID, HostName: it.HostName, ProjectDirectory: it.ProjectDirectory, Name: it.Name})
		}
		got := Labels(inputs)
		if len(got) != len(c.Want) {
			t.Errorf("%s：结果有 %d 项，期望 %d 项", c.Name, len(got), len(c.Want))
		}
		for _, it := range c.Items {
			w, ok := c.Want[strconv.Itoa(int(it.ID))]
			if !ok {
				t.Errorf("%s：对照表缺少 id %d", c.Name, it.ID)
				continue
			}
			if g := got[it.ID]; g.Text != w.Text || g.Degraded != w.Degraded {
				t.Errorf("%s：id %d 得到 %+v，期望 {%q %v}", c.Name, it.ID, g, w.Text, w.Degraded)
			}
		}
	}
}
