// Package hygiene 放仓库层面的体检测试（没有任何运行时代码）。
//
// 目前有两项：
//   - 所有 Go 源文件必须是 gofmt 干净的（见 gofmt_test.go）；
//   - Dockerfile.multistage 的多架构约定：重活固定在构建机原生平台、服务端按目标架构交叉编译（见 dockerfile_test.go，#211）。
//
// 之所以做成测试而不是只靠约定，是因为 `go test ./...` 本来就是每次提交前都会跑的闸门，
// 放在这里不需要额外的 CI，也不会被「忘了跑 gofmt」之类的事绕过。
package hygiene
