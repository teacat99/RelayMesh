package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 唤醒词模板 REST（#232 D3，D-376）：内置默认与变量表、预览。
// 会话自己的模板读写走 /workflows/:id/prompts（见 workflow_prompts.go）；全局覆盖随全局设置保存（settings.wakeTemplates），
// 保存时由 UpdateSettings 校验。这里只给 Web 登录用户用（/api/v1 下全部要求登录 JWT）。

// wakeKindInfo 是一类唤醒词的说明与内置默认。
type wakeKindInfo struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Desc 一句话说明这类唤醒词在什么时候发给谁。
	Desc string `json:"desc"`
	// Template 是内置默认（带 %变量，未渲染）；TemplateNoWorkflow 是预设没配工作流时的写法（与 Template 相同时省略）。
	Template           string `json:"template"`
	TemplateNoWorkflow string `json:"template_no_workflow,omitempty"`
	// Session 表示会话能不能给这一类写自己的模板；NeedsWorkflow 表示模板必须带 %workflow。
	Session       bool `json:"session"`
	NeedsWorkflow bool `json:"needs_workflow"`
}

func wakeKinds() []wakeKindInfo {
	one := func(kind sessionprompt.Kind, label, desc string, session, needsWf bool) wakeKindInfo {
		t, tNo := sessionprompt.Builtin(kind, true), sessionprompt.Builtin(kind, false)
		if tNo == t {
			tNo = ""
		}
		return wakeKindInfo{Kind: string(kind), Label: label, Desc: desc, Template: t, TemplateNoWorkflow: tNo, Session: session, NeedsWorkflow: needsWf}
	}
	out := []wakeKindInfo{
		one(sessionprompt.KindResume, "续聊", "Hub 在会话中断后，用非交互方式把智能体的原对话接着拉起来时发给它的话", true, false),
		one(sessionprompt.KindNew, "新开对话", "Hub 无人值守地开一个全新的智能体对话时发给它的话（它还不认识这个工作流，要靠编号接入）", true, true),
	}
	ws := wakeKindInfo{
		Kind: string(sessionprompt.KindWebStart), Label: "网页启动提示词",
		Desc:     "在网页上新建会话时，给用户复制转发给智能体的那段启动提示词（只有编号、标题、目录三个变量可用）",
		Template: store.WebWorkflowStartPrompt("%title", "%workflow", ""), NeedsWorkflow: true,
	}
	return append(out, ws)
}

// GetWakePromptDefaults GET /api/v1/wake-prompts/defaults
// 各类唤醒词的内置默认、变量表与字数上限：设置页「查看默认 / 以默认为底稿」与变量参考用。
func (h *APIHandler) GetWakePromptDefaults(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"kinds":        wakeKinds(),
		"vars":         sessionprompt.VarList(),
		"template_max": sessionprompt.TemplateMaxRunes,
	})
}

// PreviewWakePrompt POST /api/v1/wake-prompts/preview  { "kind": "resume|new|web_start", "workflow_id"?: string, "template"?: string, "preset"?: bool }
// 不保存、不发送，只告诉用户「现在发出去会是什么样」：
//   - 带 workflow_id：用这个会话此刻的真实现状渲染；不带：用示例值渲染（设置页里没有具体会话）；
//   - 带 template：渲染这份草稿（layer=draft）；不带：渲染分层选出来的那一份（会话模板 > 全局覆盖 > 内置默认）；
//   - 带 template 且 preset=true：template 是启动器预设（或会话页运行配置）里自己写的正文，排在所有分层最前面（layer=preset）。
//     它按预设的规则校验（只有字数上限，不要求带 %workflow）；正文为空时当作没写，渲染分层选出来的那一份。
//
// 模板草稿同样要过存库时的校验（字数上限、必须带 %workflow），不合规直接 400，免得用户预览通过、保存时才发现。
func (h *APIHandler) PreviewWakePrompt(c *gin.Context) {
	var req struct {
		Kind       string  `json:"kind"`
		WorkflowID string  `json:"workflow_id"`
		Template   *string `json:"template"`
		Preset     bool    `json:"preset"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：" + err.Error()})
		return
	}
	kind := sessionprompt.Kind(strings.TrimSpace(req.Kind))
	// 键入唤醒（typed）随终端会话守护一起下线（#245）：它不再是认识的类型，预览直接 400；设置里残留的旧值只是被忽略。
	if !kind.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 必须是 resume、new 或 web_start"})
		return
	}
	var draft *string
	preset := false // draft 是预设自己的正文（而不是某一层的模板草稿）
	if req.Template != nil {
		if req.Preset && kind != sessionprompt.KindWebStart {
			body := strings.TrimSpace(*req.Template)
			if n := utf8.RuneCountInString(body); n > sessionprompt.TemplateMaxRunes {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("提示词最长 %d 个字，现在是 %d 个字", sessionprompt.TemplateMaxRunes, n)})
				return
			}
			if body != "" {
				draft, preset = &body, true
			}
		} else {
			v, err := store.NormalizeWakeTemplate(kind, *req.Template)
			if err != nil {
				respondDocError(c, err)
				return
			}
			draft = &v
		}
	}
	ctx := c.Request.Context()
	wf := strings.TrimSpace(req.WorkflowID)

	var text, layer, template string
	sample := wf == ""
	switch {
	case kind == sessionprompt.KindWebStart:
		// 网页启动提示词没有分层与现状：草稿 > 全局覆盖 > 内置函数
		tpl, lay := store.WebWorkflowStartPrompt("%title", "%workflow", ""), string(store.WakeLayerBuiltin)
		if draft != nil {
			tpl, lay = *draft, string(store.WakeLayerDraft)
		} else if g := h.store.GlobalWakeTemplates(ctx).WebStart; g != "" {
			tpl, lay = g, string(store.WakeLayerGlobal)
		}
		if sample {
			text = sessionprompt.Render(tpl, sessionprompt.SampleVars())
		} else {
			text = h.store.RenderWake(ctx, store.WakeInput{Kind: kind, WorkflowID: wf, Draft: &tpl}).Text
		}
		layer, template = lay, tpl
	case sample:
		tpl, lay := "", string(store.WakeLayerDraft)
		if draft != nil {
			tpl = *draft
			if preset {
				lay = string(store.WakeLayerPreset)
			}
		} else {
			t, l := h.store.ResolveWakeTemplate(ctx, kind, "", "", true)
			tpl, lay = t, string(l)
		}
		text = sessionprompt.Render(tpl, sessionprompt.SampleVars())
		if lay != string(store.WakeLayerPreset) && (kind == sessionprompt.KindResume || kind == sessionprompt.KindNew) {
			text = sessionprompt.OneLine(text) // 与真正发送时一致：续聊 / 新开对话是命令行参数，不能含换行
		}
		layer, template = lay, tpl
	default:
		in := store.WakeInput{Kind: kind, WorkflowID: wf, Draft: draft}
		if preset {
			in.Draft, in.PresetBody = nil, *draft // 预设正文走真正发送时的那条路径（layer=preset）
		}
		res := h.store.RenderWake(ctx, in)
		text, layer, template = res.Text, string(res.Layer), res.Template
	}
	c.JSON(http.StatusOK, gin.H{
		"text": text, "layer": layer, "template": template,
		"length": len([]rune(text)), "sample": sample,
	})
}
