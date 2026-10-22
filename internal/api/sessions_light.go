package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 工作流会话表的「索引优先」模式（#169 第二期）。
//
// 背景：GET /workflows/{id}/sessions 返回整个工作流的所有行并带全文。生产上 154～368 轮的工作流是 600 KB～1 MB（压缩后），
// 其中 summary 占八成以上，而页面首屏只显示最近十几轮——更早的正文几乎没人看。light 模式把「行集合与顺序」原样保留，
// 只让较早的行不带正文：
//   - 最近一批行（见 lightCutoff）原样带全部字段；
//   - 更早的行 summary / response_text 只留第一行预览，user_messages / images 清空，并带 body_omitted: true；
//   - 完整正文按 id 另取：GET /workflows/{id}/sessions/bodies?ids=a,b,c。
//
// 不带 light 参数的请求与以前逐字节一致（经典视图、旧页面、脚本不受影响）。
// 轻量行里的预览不是正文：页面不能把它当正文渲染，body_omitted 为真时要显示骨架并去取正文。
const (
	// 预览的最大字符数。右侧轨的标题只取回复的前 48 字，会话号引用与悬浮提示只取摘要首行，所以首行预览够用。
	lightSummaryHeadRunes  = 120
	lightResponseHeadRunes = 96

	// 自动模式（不给 tail）下保留正文的尾部：至少 lightTailMinRounds 轮（预览视图的首屏窗口是 10 轮），
	// 再往前直到正文字节数会超过 lightTailBudgetBytes 或轮数达到 lightTailMaxRounds 为止。
	// 每轮短的工作流多留几轮（向上翻时不用等取数），每轮长的少留（首屏传输有上界）。
	lightTailMinRounds   = 10
	lightTailMaxRounds   = 40
	lightTailBudgetBytes = 192 << 10

	// 显式给 tail 时的上限，超过按此截断。
	lightTailExplicitMax = 1000

	// sessionBodiesMaxIDs 是 /sessions/bodies 单次最多取多少个 id。
	sessionBodiesMaxIDs = 50
)

// firstLinePreview 取文本的第一个非空行，最多 maxRunes 个字符（按字符不按字节截，不会切断多字节字符）。
func firstLinePreview(s string, maxRunes int) string {
	s = strings.TrimLeft(s, " \t\r\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, " \t\r")
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}

// heavyBytes 是一行里「正文」占的字节数（图片只是引用，很小，不计）。
func heavyBytes(s *model.FeedbackSession) int {
	n := len(s.Summary) + len(s.ResponseText)
	for _, m := range s.UserMessages {
		n += len(m)
	}
	return n
}

// lightCutoff 决定哪些行要轻量化：返回 cut，下标小于 cut 的行轻量化，其余原样。sessions 须按创建时间升序。
//
// 规则以「轮」为单位（通知不占轮次）：保留最近 K 轮，以及这 K 轮的第一轮之前、上一轮之后的间隙里的通知，
// 还有最后一轮之后的所有通知——它们都按时间落在 cut 之后。预览视图把间隙里的通知画在后一轮的轮次块里，
// 所以「最早保留的那一轮」前面的间隙也必须带正文，否则首屏窗口里会有轻量行。
//
//	tail > 0：K = tail；
//	tail = 0（自动）：K 至少 lightTailMinRounds；再往前每多一轮，看加上它的正文会不会超过字节预算、轮数是否到上限。
//
// 不足 K 轮时返回 0（整张表原样）。
func lightCutoff(sessions []model.FeedbackSession, tail int) int {
	if tail > lightTailExplicitMax {
		tail = lightTailExplicitMax
	}
	rounds, bytes := 0, 0
	for i := len(sessions) - 1; i >= 0; i-- {
		n := heavyBytes(&sessions[i])
		if sessions[i].Kind != model.FeedbackKindNotice {
			if tail > 0 {
				if rounds >= tail {
					return i + 1
				}
			} else if rounds >= lightTailMinRounds && (rounds >= lightTailMaxRounds || bytes+n > lightTailBudgetBytes) {
				return i + 1
			}
			rounds++
		}
		bytes += n
	}
	return 0
}

// lightenSession 把一行换成轻量行：正文只留首行预览，其余正文字段清空，并带 body_omitted。
func lightenSession(s *model.FeedbackSession) {
	s.Summary = firstLinePreview(s.Summary, lightSummaryHeadRunes)
	s.ResponseText = firstLinePreview(s.ResponseText, lightResponseHeadRunes)
	s.UserMessages = nil
	s.Images = nil
	s.BodyOmitted = true
}

// lightenSessions 按 lightCutoff 的规则把较早的行轻量化，返回被轻量化的行数。
func lightenSessions(sessions []model.FeedbackSession, tail int) int {
	cut := lightCutoff(sessions, tail)
	for i := 0; i < cut; i++ {
		lightenSession(&sessions[i])
	}
	return cut
}

// wantsLight 判断请求是否要轻量表（?light=1 或 true）。
func wantsLight(c *gin.Context) bool {
	v := strings.TrimSpace(c.Query("light"))
	return v == "1" || strings.EqualFold(v, "true")
}

// lightTailParam 读 ?tail=<轮数>；没给、不是数字或不是正数都按「自动」（0）。
func lightTailParam(c *gin.Context) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("tail")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sessionBody 是 /sessions/bodies 返回的一行：只有正文相关字段与定位用的 id / 版本（updated_at），
// 前端据此把正文填回缓存里同一行（只在行版本没有更新时才填，见 lib/lightRows.ts）。
type sessionBody struct {
	SessionID    string              `json:"session_id"`
	UpdatedAt    time.Time           `json:"updated_at"`
	Summary      string              `json:"summary"`
	ResponseText string              `json:"response_text"`
	UserMessages model.StringArray   `json:"user_messages"`
	Images       model.SessionImages `json:"images"`
}

// splitIDs 把 ?ids=a,b,c 拆开：去空白、去空串、去重，保持首次出现的顺序。
func splitIDs(raw string) []string {
	var ids []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// GetWorkflowSessionBodies 按 id 取一批会话的完整正文（轻量表里 body_omitted 的行用它补齐）。
// 范围限定在这个工作流里；不存在或不属于这个工作流的 id 直接略过；一次最多 sessionBodiesMaxIDs 个。
// 响应带弱 ETag：同一批 id 的内容没变时回 304（刷新页面再往上翻到同一批时省掉重传）。
func (h *APIHandler) GetWorkflowSessionBodies(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflow_id"))
	if workflowID == "" {
		workflowID = strings.TrimSpace(c.Param("id"))
	}
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id is required"})
		return
	}
	ids := splitIDs(c.Query("ids"))
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids is required"})
		return
	}
	if len(ids) > sessionBodiesMaxIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many ids (max " + strconv.Itoa(sessionBodiesMaxIDs) + ")"})
		return
	}

	rows, err := h.store.ListWorkflowSessionBodies(c.Request.Context(), workflowID, ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]sessionBody, 0, len(rows))
	for i := range rows {
		projectSessionImages(&rows[i])
		out = append(out, sessionBody{
			SessionID:    rows[i].ID,
			UpdatedAt:    rows[i].UpdatedAt,
			Summary:      rows[i].Summary,
			ResponseText: rows[i].ResponseText,
			UserMessages: rows[i].UserMessages,
			Images:       rows[i].Images,
		})
	}
	body, err := json.Marshal(gin.H{"sessions": out})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	respondConditionalJSON(c, weakETag(body), body)
}
