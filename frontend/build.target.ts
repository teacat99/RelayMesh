// 生产构建的语法目标（vite.config.ts 与单元测试共用同一份，避免两边漂移）。
//
// 不能低于 ES2021。原因是 esbuild 0.21.x 的一个缺陷：当目标不支持逻辑赋值运算符（`||=`，ES2021 才有，
// Vite 默认目标 es2020 / firefox78 就属于这种情况）且同时开启压缩时，会先把 `||=` 降级成 `x || (x = y)`，
// 又把「只有这一处引用」的 `let x;` 声明当成无用变量删掉，产物变成 `void 0||(i={})`——对未声明变量赋值。
// 严格模式（ES module）下这会直接抛 ReferenceError。
//
// 这个缺陷真实发生过：@xterm/xterm 6.0.0 的 InputHandler.requestMode（DECRQM 模式查询）被编坏，
// 而解析器回调抛异常会让 xterm.js 的写入队列永久停摆，整个终端标签页卡死。vim 9 启动时就会发 `CSI ? 12 $ p`。
// 开发服务器不压缩、目标是 esnext，所以只有生产构建才会踩到；xtermBuild.test.ts 会用同一个目标重新压缩
// 并实际执行，防止将来有人改低目标或升级工具链后悄悄回退。
//
// 引擎版本与 Vite 默认的 'modules' 基线一致，仅把 firefox78 抬到 firefox79（首个支持 `||=` 的 Firefox）。
export const BUILD_TARGET = ['es2021', 'edge88', 'firefox79', 'chrome87', 'safari14']
