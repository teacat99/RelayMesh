# 项目文档库（Project Docs）

按 **workflow** 分区存放 Markdown 文档：设计、决策（ADR）、运行手册、规格等。智能体通过 MCP 工具 **`project_docs`** 查询与增量写入；Web 端提供路径树、搜索、修订历史与导出。

## MCP 快速用法

- `overview`：篇数、路径树、最近更新、蓝图缺口
- `save`：按 `path` 新建/更新（支持 `append` / `section` + `section_body`）
- `export`：批量拉回本地（含 `INDEX.md`）
- `import` / `import_sheet`：老项目梳理或从 Workflow Sheet 复制

权限与 **`task_board`** 相同（Feedback 或 Sessions）。

## 决策卡（```decision）

正文内 fenced JSON 描述待审批决策。保存文档时，每张有效的决策卡会同步成一个**确认项**（`Qn`），你在文档页的卡片上直接作答，智能体经收件箱收到答复；`project_docs get` 返回的 `decisions[]` 带 `status` / `confirm_ref` / `answer`，`export` 会在文档末尾追加「决策结果」表。同步规则、L2 不进批量等细节见 [docs/confirm-items.md](confirm-items.md)。

## 在哪里看：项目区与老文档页（#131）

会话页头的「项目」入口（取代原来的「文档」，角标仍是这个工作流的文档篇数）：

- **工作流已归入项目**：点开就是这个项目的**知识库**（跨工作流的文档列表、搜索、模块、推荐结构）。点任意一篇，在项目页里阅读、编辑、看修订历史与评论，地址是 `/projects/<项目>/docs/<工作流>/<文档>`；「知识库」页签保持高亮，「返回知识库」回到列表。确认卡、决策汇总卡、产物预览里的文档链接同样落在这里。
- **工作流还没归入项目**：入口不消失，仍打开老的文档页 `/docs/<工作流>`，页内提示「这个工作流还没归入项目」，可以去项目页归入。
- **老链接不失效**：聊天里、书签里已有的 `/docs/<工作流>/<文档>`，在工作流属于项目时自动替换成项目内的地址（保留查询参数与锚点，不多留一条历史记录，后退不会被弹回来）。查所属项目 3 秒内没有结果或失败，就留在老文档页——老页永远是兜底。
- 所有「打开一篇文档」的入口都走同一个跳转函数（`frontend/src/lib/docNavClient.ts` 的 `openDoc`），「点文档跳到哪」只在 `docNav.ts` 里改。

## 老文档并入知识库（迁移）

知识库只认已并入项目的文档（`project_id > 0`）。工作流归入项目**之前**写的文档还留在工作流里：知识库列表、搜索、模块里看不到它们，但它们没有丢，可以照常打开阅读与编辑。项目页的「迁移」页签把它们并入：

1. 进入「迁移」页就自动预览（只读，不改任何数据）：逐篇列出标题、原路径和去向——路径不变；会话工作表放到 `sessions/<工作流>/`；不同工作流撞路径的放到 `legacy/<工作流>/`。知识库页顶部也会提示「还有 N 篇留在工作流里」，并直接列出它们。
2. 确认时带上预览的哈希：两次之间数据变了会被拒绝，要求重新预览。迁移不会自动发生，一定由人确认。
3. 迁移只改 `project_id`（和少数撞路径 / 会话工作表的 `path`）；文档编号、修订历史、所属工作流、更新时间都不变。
4. 审计事件 `project.migrate` 记下 `doc_ids`（这次并入的全部文档编号）和 `moves`（改过路径的：编号、原路径、新路径）。**要还原**：把 `doc_ids` 里每篇文档的 `project_id` 置回 0，把 `moves` 里每篇的 `path` 改回原路径即可，不需要备份恢复。查询：`GET /api/v1/audit?project=<项目>&action=project.migrate`（`detail` 是 JSON 字符串）。

## REST

`/api/v1/workflows/:workflow_id/docs…`（需 Web JWT）。评论走收件箱，不并入阻塞中的反馈会话。项目维度：`/api/v1/projects/:id/docs…`（知识库）、`/api/v1/projects/:id/migration`（GET 预览、POST `{hash}` 确认）、`/api/v1/workflows/:workflow_id/project`（工作流属于哪个项目）。
