package web

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

// 内嵌的前端产物与它的 Brotli 预压缩副本必须成对出现（#186）。
//
// frontend/build.precompress.ts 在每次 `pnpm run build` 写完 web/dist 后，给 assets 下 ≥ 1024 字节的 .js / .css 各写一个 .br；
// 服务端（internal/api/precompressed.go）在浏览器声明接受 br 时直接发它。web/dist 是提交进仓库的：
//   - 有人用 RM_SKIP_PRECOMPRESS=1 或别的方式构建后把产物提交进来，.br 就缺了——大文件会悄悄退回动态 gzip，首访又多下约 20%；
//   - 重新构建后旧的 .br 没清掉，就会有「孤儿」占着二进制体积。
//
// 这里只检查成对关系与收益；解压后是否与原文件逐字节一致由构建时的自检保证（Go 标准库没有 Brotli 解码器，不为一个测试引入依赖）。
const (
	minBytes = 1024 // 与 frontend/build.precompress.ts 的 PRECOMPRESS_MIN_BYTES 保持一致
	maxRatio = 0.9  // 与 PRECOMPRESS_MIN_GAIN（10%）一致：.br 必须比原文件至少小这么多
)

func TestEmbeddedBrotliSiblings(t *testing.T) {
	sub, err := fs.Sub(FS, "dist")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil {
		t.Fatalf("读不到 dist/assets: %v", err)
	}
	size := map[string]int64{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		size[e.Name()] = info.Size()
	}
	if len(size) == 0 {
		t.Fatal("dist/assets 是空的：前端还没有构建")
	}

	compressible, paired := 0, 0
	for name, n := range size {
		if strings.HasSuffix(name, ".br") {
			orig := strings.TrimSuffix(name, ".br")
			rawSize, ok := size[orig]
			if !ok {
				t.Errorf("孤儿 %s：原文件 %s 不在 dist/assets 里（旧构建的残留，重新 pnpm run build）", name, orig)
				continue
			}
			if float64(n) > float64(rawSize)*maxRatio {
				t.Errorf("%s 只比原文件小 %.1f%%（%d → %d 字节）：应至少小 10%%", name, 100*(1-float64(n)/float64(rawSize)), rawSize, n)
			}
			continue
		}
		if ext := path.Ext(name); (ext != ".js" && ext != ".css") || n < minBytes {
			continue
		}
		compressible++
		if _, ok := size[name+".br"]; ok {
			paired++
		} else {
			t.Errorf("%s（%d 字节）没有 %s.br：web/dist 不是用 pnpm run build 构建的（或设了 RM_SKIP_PRECOMPRESS=1）", name, n, name)
		}
	}
	if compressible == 0 {
		t.Fatal("dist/assets 里没有任何 .js / .css，检查前端构建")
	}
	t.Logf("%d 个 .js / .css 产物，%d 个带 .br", compressible, paired)
}
