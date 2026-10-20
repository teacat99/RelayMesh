package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 条件请求（#169 / P4）：体积大、读多写少的 JSON 列表带弱 ETag，If-None-Match 命中就回 304、不写正文也不压缩。
//
// 为什么要它：工作流的会话整表（GET /workflows/{id}/sessions）在生产上是 600 KB～1 MB（压缩后），而页面会因为刷新、重连、
// 离开 30 秒以上切回来等原因反复整表同步，绝大多数时候内容根本没变。浏览器的 HTTP 缓存认得 ETag 与 Cache-Control: no-cache：
// 拿到 304 就透明地用回缓存里的那一份，页面代码一行都不用改。

// weakETag 返回内容的弱校验符 W/"<sha256 的前 16 字节>"。
// 用弱的：同一份内容经 gzip 传输（Vary: Accept-Encoding）字节不同，强校验符会被严格的缓存判为不匹配。
func weakETag(content []byte) string {
	sum := sha256.Sum256(content)
	return `W/"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches 判断请求头 If-None-Match 是否命中 etag（RFC 7232 §3.2 的弱比较：忽略 W/ 前缀；支持 * 与逗号分隔的多个）。
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" || etag == "" {
		return false
	}
	if header == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(part), "W/") == want {
			return true
		}
	}
	return false
}

// respondConditionalJSON 写出已经序列化好的 JSON：带 ETag 与 Cache-Control: private, no-cache（可以缓存，但每次必须向服务器校验），
// If-None-Match 命中时返回 304 且没有正文。
func respondConditionalJSON(c *gin.Context, etag string, body []byte) {
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, no-cache")
	if etagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// marshalSessionsPayload 序列化「{"sessions": [...]}」整表响应（与 c.JSON(200, gin.H{"sessions": sessions}) 逐字节一致），
// 并给出它的内容版本 ETag。
//
// 版本不含 last_keepalive_at：它在 Agent 等待期间每次心跳都变，算进去的话，Agent 在线时每次打开页面都得重传整表；
// 页面上这个字段由 /sessions/current 与 SSE 校准。is_mcp_active / mcp_active_at / mcp_timeout_seconds 在一个等待者的生命周期内不变，
// 它们入版本——等待者开始 / 结束会让版本变化，缓存里的整表不会长期带着过时的「Agent 在线」。
// 其余字段（含正文、状态、各种时间戳）全部入版本：以内容为准，不靠「updated_at 没变所以没变」这类推断，
// 任何一条写路径忘了刷新 updated_at 也不会让页面拿到过期的 304。
func marshalSessionsPayload(sessions []model.FeedbackSession) (body []byte, etag string, err error) {
	marshal := func() ([]byte, error) { return json.Marshal(gin.H{"sessions": sessions}) }

	var withKeepalive []int
	for i := range sessions {
		if sessions[i].LastKeepaliveAt != nil {
			withKeepalive = append(withKeepalive, i)
		}
	}
	if len(withKeepalive) == 0 {
		// 常见情形（没有 Agent 在等）：正文就是版本，只序列化一次
		body, err = marshal()
		if err != nil {
			return nil, "", err
		}
		return body, weakETag(body), nil
	}

	saved := make([]*time.Time, len(withKeepalive))
	for k, i := range withKeepalive {
		saved[k] = sessions[i].LastKeepaliveAt
		sessions[i].LastKeepaliveAt = nil
	}
	version, err := marshal()
	for k, i := range withKeepalive {
		sessions[i].LastKeepaliveAt = saved[k]
	}
	if err != nil {
		return nil, "", err
	}
	body, err = marshal()
	if err != nil {
		return nil, "", err
	}
	return body, weakETag(version), nil
}
