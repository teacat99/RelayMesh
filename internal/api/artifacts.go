package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const (
	ticketPurposeArtifact = "artifact"
	artifactTicketTTL     = 60 * time.Second
)

// artifactCSP 是 HTML Artifact 渲染页的响应头：页面在不透明源里运行（sandbox 不含 allow-same-origin），
// 拿不到 cookie / localStorage / 宿主 DOM；不能发起任何网络请求；只能用内联脚本与样式、data / blob 图片。
// 不用 srcdoc：srcdoc 文档会继承宿主的 CSP，宿主的 script-src 'self' 会把内联脚本拦掉。
const artifactCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

func artifactDocID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文档编号无效"})
		return 0, false
	}
	return uint(n), true
}

// IssueArtifactTicket POST /api/v1/artifacts/:id/ticket：签发渲染页的一次性票据（iframe 不能带请求头）。
func (a *AuthHandler) IssueArtifactTicket(c *gin.Context) {
	id, ok := artifactDocID(c)
	if !ok {
		return
	}
	doc, err := a.store.GetProjectDocByID(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return
	}
	if doc.Render != model.DocRenderHTML {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有 HTML Artifact 才有渲染页"})
		return
	}
	base := "/api/v1/artifacts/" + c.Param("id") + "/render"
	_, currentPass, credsOK := a.credentialsOrUnavailable(c)
	if !credsOK {
		respondCredentialsUnavailable(c)
		return
	}
	if currentPass == "" {
		c.JSON(http.StatusOK, gin.H{"url": base, "expires_in": 0})
		return
	}
	subject := "admin"
	if v, ok := c.Get("auth_subject"); ok {
		if str, isStr := v.(string); isStr && str != "" {
			subject = str
		}
	}
	ticket, err := a.tickets.Issue(ticketPurposeArtifact, subject, c.Param("id"), artifactTicketTTL)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "票据过多，请稍后再试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": base + "?ticket=" + ticket, "expires_in": int(artifactTicketTTL.Seconds())})
}

// ArtifactRenderMiddleware 只认 ?ticket=（单次、绑定文档编号）。没开登录口令时放行。
func (a *AuthHandler) ArtifactRenderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, currentPass, credsOK := a.credentialsOrUnavailable(c)
		if !credsOK {
			respondCredentialsUnavailable(c)
			return
		}
		if currentPass == "" {
			c.Next()
			return
		}
		if info, ok := a.tickets.Redeem(c.Query("ticket"), ticketPurposeArtifact); ok && info.Bind == c.Param("id") {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "票据无效或已过期", "auth_required": true})
	}
}

var (
	headOpenRe = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlOpenRe = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	bodyOpenRe = regexp.MustCompile(`(?i)<body(\s[^>]*)?>`)
)

// 渲染页里注入的桥脚本：只通过 postMessage 与宿主说话；宿主按白名单处理，并以「查看者」身份代为鉴权。
const artifactBridge = `<script>(function(){var P={},n=0;function call(t,k,v,e){return new Promise(function(res,rej){var id=++n;P[id]={res:res,rej:rej};` +
	`parent.postMessage({rm:1,id:id,type:t,key:k,value:v,expected:e},'*')})}` +
	`window.addEventListener('message',function(ev){if(ev.source!==parent)return;var d=ev.data;if(!d||d.rm!==1)return;` +
	`if(d.type==='data.changed'){try{window.dispatchEvent(new CustomEvent('relaymesh:data',{detail:{key:d.key}}))}catch(x){}return}` +
	`var p=P[d.id];if(!p)return;delete P[d.id];if(d.ok)p.res(d.value);else p.rej(new Error(d.error||'failed'))});` +
	`window.relaymesh={data:{get:function(k){return call('data.get',k)},set:function(k,v,e){return call('data.set',k,v,e)},` +
	`list:function(){return call('data.list')},remove:function(k){return call('data.delete',k)}},` +
	`comment:function(text,anchor){return call('comment',anchor,text)}};` +
	`function rs(){try{parent.postMessage({rm:1,type:'resize',height:Math.ceil(document.documentElement.scrollHeight)},'*')}catch(x){}}` +
	`if(window.ResizeObserver){new ResizeObserver(rs).observe(document.documentElement)}window.addEventListener('load',rs);` +
	`document.addEventListener('DOMContentLoaded',rs)})()</script>`

// injectArtifactBridge 把桥脚本放进文档里：优先 <head> 之后，其次 <html> 之后，再次 <body> 之前，都没有就放最前。
func injectArtifactBridge(body string) string {
	for _, re := range []*regexp.Regexp{headOpenRe, htmlOpenRe} {
		if loc := re.FindStringIndex(body); loc != nil {
			return body[:loc[1]] + artifactBridge + body[loc[1]:]
		}
	}
	if loc := bodyOpenRe.FindStringIndex(body); loc != nil {
		return body[:loc[0]] + artifactBridge + body[loc[0]:]
	}
	return artifactBridge + body
}

// RenderArtifact GET /api/v1/artifacts/:id/render?ticket=
func (h *APIHandler) RenderArtifact(c *gin.Context) {
	id, ok := artifactDocID(c)
	if !ok {
		return
	}
	doc, err := h.store.GetProjectDocByID(c.Request.Context(), id)
	if err != nil || doc.DeletedAt != nil || doc.Render != model.DocRenderHTML {
		c.String(http.StatusNotFound, "没有这个 Artifact")
		return
	}
	hd := c.Writer.Header()
	hd.Set("Content-Security-Policy", artifactCSP)
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injectArtifactBridge(doc.Body)))
}

// GetArtifact GET /api/v1/artifacts/:id：聊天里的引用卡片和右侧预览用的摘要。HTML 的正文不在这里返回（走沙箱渲染页）。
func (h *APIHandler) GetArtifact(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok || doc.DeletedAt != nil {
		if ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "这篇文档已被归档或删除"})
		}
		return
	}
	out := gin.H{
		"id": doc.ID, "title": doc.Title, "path": doc.Path, "kind": doc.Kind, "render": doc.Render, "rev": doc.Rev,
		"workflow_id": doc.WorkflowID, "project_id": doc.ProjectID, "module_path": doc.ModulePath, "updated_at": doc.UpdatedAt,
		"open_comments": h.store.CountOpenArtifactComments(c.Request.Context(), doc.ID),
	}
	if doc.Render != model.DocRenderHTML {
		out["body"] = doc.Body
	}
	c.JSON(http.StatusOK, out)
}

// ---- 数据 ----

func (h *APIHandler) artifactDoc(c *gin.Context) (*model.ProjectDoc, bool) {
	id, ok := artifactDocID(c)
	if !ok {
		return nil, false
	}
	doc, err := h.store.GetProjectDocByID(c.Request.Context(), id)
	if err != nil {
		respondDocError(c, err)
		return nil, false
	}
	return doc, true
}

// broadcastArtifact 推送 Artifact 的数据 / 评论变化。数据事件带版本号（删除带 deleted），
// 渲染框据此忽略页面自己刚写的那一次，避免「写 → 通知 → 页面再写」的回声循环。
func (h *APIHandler) broadcastArtifact(event string, doc *model.ProjectDoc, key string, version int, deleted bool) {
	if h.broker == nil {
		return
	}
	h.broker.Broadcast(event, gin.H{"doc_id": doc.ID, "workflow_id": doc.WorkflowID, "project_id": doc.ProjectID, "key": key, "version": version, "deleted": deleted})
}

// ListArtifactData GET /api/v1/artifacts/:id/data
func (h *APIHandler) ListArtifactData(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	rows, err := h.store.ArtifactDataList(c.Request.Context(), doc.ID)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// GetArtifactData GET /api/v1/artifacts/:id/data/:key
func (h *APIHandler) GetArtifactData(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	row, err := h.store.ArtifactDataGet(c.Request.Context(), doc.ID, c.Param("key"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": row})
}

// PutArtifactData PUT /api/v1/artifacts/:id/data/:key  {value: <任意 JSON>, expected_version?: n}
func (h *APIHandler) PutArtifactData(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	var req struct {
		Value           rawJSON `json:"value"`
		ExpectedVersion *int    `json:"expected_version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Value) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对：需要 value"})
		return
	}
	row, err := h.store.ArtifactDataSet(c.Request.Context(), store.ArtifactDataSetInput{
		DocID: doc.ID, Key: c.Param("key"), ValueJSON: string(req.Value), ExpectedVersion: req.ExpectedVersion,
		ActorLabel: "你", ActorRef: "human",
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	// 审计不记值，只记谁改了哪个键、改到第几版。
	h.humanAudit(c, doc.ProjectID, model.AuditArtifactDataSet, "doc", doc.Path, row.Key, map[string]any{"version": row.Version, "bytes": len(row.Value)})
	h.broadcastArtifact("artifact_data_updated", doc, row.Key, row.Version, false)
	c.JSON(http.StatusOK, gin.H{"item": row})
}

// DeleteArtifactData DELETE /api/v1/artifacts/:id/data/:key
func (h *APIHandler) DeleteArtifactData(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	deleted, err := h.store.ArtifactDataDelete(c.Request.Context(), doc.ID, c.Param("key"))
	if err != nil {
		respondDocError(c, err)
		return
	}
	if deleted {
		h.humanAudit(c, doc.ProjectID, model.AuditArtifactDataSet, "doc", doc.Path, c.Param("key"), map[string]any{"deleted": true})
		h.broadcastArtifact("artifact_data_updated", doc, c.Param("key"), 0, true)
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// ---- 评论 ----

// ListArtifactComments GET /api/v1/artifacts/:id/comments?resolved=1
func (h *APIHandler) ListArtifactComments(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	rows, err := h.store.ListArtifactComments(c.Request.Context(), doc.ID, c.Query("resolved") == "1", 0)
	if err != nil {
		respondDocError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// PostArtifactComment POST /api/v1/artifacts/:id/comments  {anchor?, text}
// 评论存库（Agent 用 project_docs comments 读），同时给文档所属的工作流发一条追加消息，让负责的 Agent 在下次调用时收到。
func (h *APIHandler) PostArtifactComment(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	var req struct {
		Anchor string `json:"anchor"`
		Text   string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	cm, err := h.store.AddArtifactComment(c.Request.Context(), store.ArtifactCommentInput{
		DocID: doc.ID, Anchor: req.Anchor, Body: req.Text, AuthorKind: "human", AuthorRef: "human", AuthorLabel: "你",
	})
	if err != nil {
		respondDocError(c, err)
		return
	}
	msg := fmt.Sprintf("【文档评论】《%s》", doc.Title)
	if cm.Anchor != "" {
		msg += "#" + cm.Anchor
	}
	msg += "：" + cm.Body + fmt.Sprintf("（评论 #%d，可用 project_docs comments 读取并标记解决）", cm.ID)
	_, _ = h.store.QueueWorkflowFeedback(c.Request.Context(), store.QueueFeedbackInput{WorkflowID: doc.WorkflowID, ResponseText: strings.TrimSpace(msg)})
	h.broadcastArtifact("artifact_comment_updated", doc, "", 0, false)
	c.JSON(http.StatusOK, gin.H{"item": cm})
}

// ResolveArtifactComment POST /api/v1/artifacts/:id/comments/:cid/resolve  {resolved: bool}
func (h *APIHandler) ResolveArtifactComment(c *gin.Context) {
	doc, ok := h.artifactDoc(c)
	if !ok {
		return
	}
	cid, err := strconv.ParseUint(c.Param("cid"), 10, 32)
	if err != nil || cid == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评论编号无效"})
		return
	}
	var req struct {
		Resolved *bool `json:"resolved"`
	}
	_ = c.ShouldBindJSON(&req)
	resolved := req.Resolved == nil || *req.Resolved
	cm, err := h.store.ResolveArtifactComment(c.Request.Context(), doc.ID, uint(cid), resolved)
	if err != nil {
		respondDocError(c, err)
		return
	}
	h.broadcastArtifact("artifact_comment_updated", doc, "", 0, false)
	c.JSON(http.StatusOK, gin.H{"item": cm})
}

// rawJSON 原样保留请求里的 JSON 片段（value 可以是对象、数组、字符串、数字、布尔或 null）。
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}
