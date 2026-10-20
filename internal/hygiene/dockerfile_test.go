package hygiene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dockerStage 是多阶段 Dockerfile 里的一个构建阶段。
type dockerStage struct {
	from string   // 该阶段的 FROM 指令（整行）
	ins  []string // 阶段内其余指令：续行已合并成一行，注释与空行已去掉
}

// parseDockerStages 按 `FROM ... AS <名字>` 把 Dockerfile 切成阶段（键是阶段名）。
// 只做这几个测试需要的最小解析：去注释、合并反斜杠续行、识别 FROM。
func parseDockerStages(t *testing.T, path string) map[string]*dockerStage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 %s（%v），跳过", path, err)
	}
	stages := map[string]*dockerStage{}
	var cur *dockerStage
	add := func(ins string) {
		ins = strings.TrimSpace(ins)
		if ins == "" {
			return
		}
		fields := strings.Fields(ins)
		if strings.EqualFold(fields[0], "FROM") {
			cur = &dockerStage{from: ins}
			for i := 1; i+1 < len(fields); i++ {
				if strings.EqualFold(fields[i], "AS") {
					stages[fields[i+1]] = cur
				}
			}
			return
		}
		if cur != nil {
			cur.ins = append(cur.ins, ins)
		}
	}
	pending := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue // 注释（续行中间的注释行也不算指令的一部分）
		}
		if strings.HasSuffix(trim, "\\") {
			pending += strings.TrimSuffix(trim, "\\") + " "
			continue
		}
		add(pending + trim)
		pending = ""
	}
	add(pending)
	return stages
}

func multistageStages(t *testing.T) map[string]*dockerStage {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("找不到仓库根目录的 go.mod（%s），跳过: %v", root, err)
	}
	stages := parseDockerStages(t, filepath.Join(root, "Dockerfile.multistage"))
	for _, name := range []string{"frontend-builder", "backend-builder", "agent-builder", "runner"} {
		if stages[name] == nil {
			t.Fatalf("Dockerfile.multistage 里找不到阶段 %q（阶段改名了？请同步这个测试）", name)
		}
	}
	return stages
}

// TestMultistageBuildersRunOnBuildPlatform 保证多架构镜像里「重活」都在构建机原生平台上跑。
//
// 背景（#211）：前端构建、服务端编译、Agent 交叉编译如果也按目标架构跑，arm64 那一路就要在 QEMU 里
// 跑 npm ci / vite / go build，比原生慢得多（发 v2.0.0-rc.1 时，这一步在 GitHub Actions 上远远超过之前版本的
// 10～15 分钟）。只有最后的 runner 阶段（装证书、建用户）必须按目标架构，因为它决定镜像的 CPU 架构。
func TestMultistageBuildersRunOnBuildPlatform(t *testing.T) {
	stages := multistageStages(t)
	for _, name := range []string{"frontend-builder", "backend-builder", "agent-builder"} {
		if from := stages[name].from; !strings.Contains(from, "--platform=$BUILDPLATFORM") {
			t.Errorf("阶段 %s 必须固定在构建机原生平台（FROM --platform=$BUILDPLATFORM …），否则多架构构建会在 QEMU 里跑它: %s", name, from)
		}
	}
	if from := stages["runner"].from; strings.Contains(from, "--platform") {
		t.Errorf("runner 阶段必须按目标架构（不要写 --platform），它决定镜像的 CPU 架构: %s", from)
	}
}

// TestMultistageServerBinaryCrossCompilesToTarget 保证服务端二进制是按「目标架构」交叉编译的。
//
// 服务端阶段改成跑在构建机原生平台之后，如果 go build 没有带上 GOARCH，arm64 镜像里装进去的就是 amd64 的二进制：
// 构建照样成功，镜像照样推送，只有在 arm64 机器上启动时才会报「exec format error」——这是最难发现的一类错误，所以单独守住。
func TestMultistageServerBinaryCrossCompilesToTarget(t *testing.T) {
	st := multistageStages(t)["backend-builder"]

	declared := map[string]bool{}
	build := ""
	for _, in := range st.ins {
		fields := strings.Fields(in)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "ARG") {
			declared[strings.SplitN(fields[1], "=", 2)[0]] = true
		}
		if len(fields) >= 2 && strings.EqualFold(fields[0], "ENV") && (strings.Contains(in, "GOOS") || strings.Contains(in, "GOARCH")) {
			t.Errorf("backend-builder 不能用 ENV 固定 GOOS / GOARCH（算指纹的 go run 要在构建机原生平台上跑）: %s", in)
		}
		if strings.Contains(in, "go build") && strings.Contains(in, "./cmd/relaymesh") && !strings.Contains(in, "./cmd/relaymesh-") {
			build = in
		}
	}
	for _, arg := range []string{"TARGETOS", "TARGETARCH"} {
		if !declared[arg] {
			t.Errorf("backend-builder 需要声明 ARG %s，BuildKit 才会把目标平台传进这个阶段", arg)
		}
	}
	if build == "" {
		t.Fatal("backend-builder 里找不到编译服务端的 go build 指令")
	}
	if !strings.Contains(build, `GOOS="${TARGETOS`) {
		t.Errorf("服务端的 go build 要带 GOOS=\"${TARGETOS…}\": %s", build)
	}
	if !strings.Contains(build, `GOARCH="${TARGETARCH`) {
		t.Errorf("服务端的 go build 要带 GOARCH=\"${TARGETARCH…}\"，否则 arm64 镜像里会装进 amd64 的二进制: %s", build)
	}
	// 算指纹的 go run 在构建机原生平台上跑：GOOS / GOARCH 只能出现在它之后（只作用于后面的 go build）
	run := strings.Index(build, "go run ./cmd/agentrev")
	if run < 0 {
		t.Fatalf("服务端的编译指令里找不到算指纹的 go run ./cmd/agentrev: %s", build)
	}
	if i := strings.Index(build, "GOARCH="); i >= 0 && i < run {
		t.Errorf("GOARCH 不能出现在 go run ./cmd/agentrev 之前，否则算指纹的程序会被编成目标架构、在构建机上跑不起来: %s", build)
	}
}
