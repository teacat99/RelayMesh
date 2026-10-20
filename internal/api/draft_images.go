package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 草稿图片（云存储）：粘贴即上传，草稿 JSON 里只留引用；提交时会话直接复用这份正文，草稿销毁时删除。
// 它只服务网页端的输入区——MCP 看到的永远是会话里的图片。

var (
	// 内容键由前端生成（长度的 36 进制 + 哈希），只放行安全字符，避免它出现在路径 / 日志里时带来麻烦。
	draftImageKeyRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
	draftImageFormatRe = regexp.MustCompile(`^[a-z0-9+.-]{1,16}$`)
)

const (
	maxWorkflowIDLen = 128
	maxImageNameLen  = 255
	// 请求体上限 = 单张图片正文上限 + 元数据的余量。
	maxDraftImageBody = store.MaxDraftImageChars + 64<<10

	// 后台清理：遗弃超过这么久、且已保存的草稿不再引用的草稿图片才会被删（正常路径是客户端在草稿销毁时主动删除）。
	draftImageOrphanAge      = 24 * time.Hour
	draftImageSweepInterval  = time.Hour
	draftImageSweepFirstWait = 5 * time.Minute
)

func validWorkflowID(id string) bool {
	return id != "" && len(id) <= maxWorkflowIDLen && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00\r\n")
}

func draftImageParams(c *gin.Context) (workflowID, key string, ok bool) {
	workflowID = strings.TrimSpace(c.Param("workflow_id"))
	if !validWorkflowID(workflowID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "工作流标识不合法", "code": "invalid_workflow"})
		return "", "", false
	}
	if k := c.Param("key"); k != "" {
		if !draftImageKeyRe.MatchString(k) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "图片键不合法", "code": "invalid_key"})
			return "", "", false
		}
		key = k
	}
	return workflowID, key, true
}

// stripDataURLPrefix 去掉 data:image/png;base64, 这样的前缀，只留 Base64 正文。
func stripDataURLPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

// validBase64 流式校验 Base64，不为几十 MB 的正文再分配一份解码后的缓冲。
func validBase64(s string) bool {
	if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(s))); err == nil {
		return true
	}
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.RawStdEncoding, strings.NewReader(s)))
	return err == nil
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

type draftImageBody struct {
	Name     string `json:"name"`
	Format   string `json:"format"`
	DataType string `json:"data_type"`
	Hash     string `json:"hash"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Data     string `json:"data"`
}

func writeDraftImageStoreError(c *gin.Context, err error) {
	var app *store.AppError
	if errors.As(err, &app) && app.Kind == store.ErrInvalidInput {
		c.JSON(http.StatusBadRequest, gin.H{"error": app.Message, "code": "draft_image_rejected"})
		return
	}
	log.Printf("[draft-images] 存储失败: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "草稿图片存储失败，请查看服务端日志"})
}

// PutDraftImage 上传（或重新确认）一张草稿图片。幂等：同一个内容键重复上传只刷新写入时间。
func (h *APIHandler) PutDraftImage(c *gin.Context) {
	workflowID, key, ok := draftImageParams(c)
	if !ok || key == "" {
		if ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少图片键", "code": "invalid_key"})
		}
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDraftImageBody)
	var body draftImageBody
	if err := c.ShouldBindJSON(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "图片太大，单张草稿图片不能超过 30 MB", "code": "draft_image_too_large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容不合法"})
		return
	}
	data := stripDataURLPrefix(body.Data)
	if data == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片内容为空", "code": "draft_image_rejected"})
		return
	}
	if !validBase64(data) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片内容不是合法的 Base64", "code": "draft_image_rejected"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(body.Format))
	if !draftImageFormatRe.MatchString(format) {
		format = ""
	}
	hash := strings.TrimSpace(body.Hash)
	if len(hash) > 64 {
		hash = hash[:64]
	}
	if hash == "" {
		sum := sha256.Sum256([]byte(data))
		hash = hex.EncodeToString(sum[:8])
	}
	img := model.DraftImage{
		WorkflowID: workflowID,
		Key:        key,
		Name:       truncateRunes(strings.TrimSpace(body.Name), maxImageNameLen),
		Format:     format,
		DataType:   "base64",
		Hash:       hash,
		Width:      clampDimension(body.Width),
		Height:     clampDimension(body.Height),
		Data:       data,
	}
	if err := h.store.PutDraftImage(c.Request.Context(), img); err != nil {
		writeDraftImageStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "image": store.DraftImageInfo{
		Key: img.Key, Name: img.Name, Format: img.Format, Hash: img.Hash, Width: img.Width, Height: img.Height, Size: len(img.Data), UpdatedAt: time.Now(),
	}})
}

func clampDimension(v int) int {
	if v < 0 {
		return 0
	}
	if v > 1<<20 {
		return 1 << 20
	}
	return v
}

// GetDraftImage 取回一张草稿图片（含正文），用于另一台设备 / 本机缓存被清理之后恢复草稿里的图片。
func (h *APIHandler) GetDraftImage(c *gin.Context) {
	workflowID, key, ok := draftImageParams(c)
	if !ok {
		return
	}
	img, err := h.store.GetDraftImage(c.Request.Context(), workflowID, key)
	if err != nil {
		writeDraftImageStoreError(c, err)
		return
	}
	if img == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "草稿图片不存在", "code": "draft_image_not_found"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"image": img})
}

// ListDraftImages 列出一个工作流的草稿图片（只有元数据）：页面载入时一次请求就能知道哪些图片已经在服务器上。
func (h *APIHandler) ListDraftImages(c *gin.Context) {
	workflowID, _, ok := draftImageParams(c)
	if !ok {
		return
	}
	list, err := h.store.ListDraftImages(c.Request.Context(), workflowID)
	if err != nil {
		writeDraftImageStoreError(c, err)
		return
	}
	if list == nil {
		list = []store.DraftImageInfo{}
	}
	c.JSON(http.StatusOK, gin.H{"images": list})
}

// DeleteDraftImage 删除一张草稿图片（从草稿里移除图片、草稿被销毁时由前端调用）。图片不存在也算成功。
func (h *APIHandler) DeleteDraftImage(c *gin.Context) {
	workflowID, key, ok := draftImageParams(c)
	if !ok || key == "" {
		if ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少图片键", "code": "invalid_key"})
		}
		return
	}
	n, err := h.store.DeleteDraftImages(c.Request.Context(), workflowID, []string{key})
	if err != nil {
		writeDraftImageStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": n})
}

// ---------- 提交时复用草稿图片 ----------

// feedbackImage 是提交反馈时的一张图片：要么自带正文（旧路径），要么只带 draft_key，由服务端从草稿图片里取正文。
type feedbackImage struct {
	model.SessionImage
	DraftKey string `json:"draft_key,omitempty"`
}

// draftImageMissingError 表示请求引用的草稿图片已经不在服务器上（被清理、被另一台设备删除）。
type draftImageMissingError struct{ Keys []string }

func (e *draftImageMissingError) Error() string { return "草稿图片已不在服务器上" }

// resolveFeedbackImages 把引用草稿图片的条目换成带正文的会话图片，并返回被复用的草稿图片键（提交成功后删除）。
// 请求里自带正文的图片原样保留；同时带了正文和 draft_key 时以正文为准，但这个键同样算已复用。
func (h *APIHandler) resolveFeedbackImages(ctx context.Context, workflowID string, in []feedbackImage) ([]model.SessionImage, []string, error) {
	out := make([]model.SessionImage, 0, len(in))
	var keys []string
	seen := map[string]struct{}{}
	for _, img := range in {
		if img.DraftKey == "" {
			continue
		}
		if !draftImageKeyRe.MatchString(img.DraftKey) {
			return nil, nil, &draftImageMissingError{Keys: []string{img.DraftKey}}
		}
		if _, dup := seen[img.DraftKey]; !dup {
			seen[img.DraftKey] = struct{}{}
			keys = append(keys, img.DraftKey)
		}
	}
	var rows map[string]model.DraftImage
	if len(keys) > 0 {
		if !validWorkflowID(workflowID) {
			return nil, nil, &draftImageMissingError{Keys: keys}
		}
		var err error
		rows, err = h.store.ResolveDraftImages(ctx, workflowID, keys)
		if err != nil {
			return nil, nil, err
		}
	}
	var missing []string
	for _, img := range in {
		s := img.SessionImage
		if img.DraftKey != "" && strings.TrimSpace(s.Data) == "" {
			row, ok := rows[img.DraftKey]
			if !ok || row.Data == "" {
				missing = append(missing, img.DraftKey)
				continue
			}
			s.Data = row.Data
			if s.DataType == "" {
				s.DataType = "base64"
			}
			if s.Name == "" {
				s.Name = row.Name
			}
			if s.Format == "" {
				s.Format = row.Format
			}
			if s.Hash == "" {
				s.Hash = row.Hash
			}
			if s.Width == 0 {
				s.Width = row.Width
			}
			if s.Height == 0 {
				s.Height = row.Height
			}
		}
		out = append(out, s)
	}
	if len(missing) > 0 {
		return nil, nil, &draftImageMissingError{Keys: missing}
	}
	return out, keys, nil
}

// writeResolveError 把 resolveFeedbackImages 的错误写成响应：缺失走 400 + 机器可读的 code，客户端据此改用自带正文重试。
func writeResolveError(c *gin.Context, err error) {
	var missing *draftImageMissingError
	if errors.As(err, &missing) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "有草稿图片已经不在服务器上，请重新附上这些图片", "code": "draft_image_missing", "keys": missing.Keys})
		return
	}
	log.Printf("[draft-images] 读取草稿图片失败: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "读取草稿图片失败，请查看服务端日志"})
}

// releaseConsumedDraftImages 提交成功之后删除被复用的草稿图片。失败只记日志：后台清理会兜底，不能因此让已成功的提交报错。
func (h *APIHandler) releaseConsumedDraftImages(ctx context.Context, workflowID string, keys []string) {
	if len(keys) == 0 {
		return
	}
	if _, err := h.store.DeleteDraftImages(ctx, workflowID, keys); err != nil {
		log.Printf("[draft-images] 清理已复用的草稿图片失败（workflow=%s, %d 张）: %v", workflowID, len(keys), err)
	}
}

// ---------- 后台清理 ----------

// startDraftImageMaintenance 周期性清理遗弃的草稿图片。重复调用无副作用；Shutdown 时随 stopDraftImageMaintenance 退出。
func (s *Server) startDraftImageMaintenance() {
	s.draftMaintMu.Lock()
	defer s.draftMaintMu.Unlock()
	if s.draftMaintCancel != nil || s.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.draftMaintCancel = cancel
	go func() {
		timer := time.NewTimer(draftImageSweepFirstWait)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if n, err := s.store.SweepDraftImages(ctx, time.Now().Add(-draftImageOrphanAge)); err != nil {
				log.Printf("[draft-images] 清理遗弃的草稿图片失败: %v", err)
			} else if n > 0 {
				log.Printf("[draft-images] 已清理 %d 张遗弃的草稿图片（超过 %s 没有被任何已保存的草稿引用）", n, draftImageOrphanAge)
			}
			timer.Reset(draftImageSweepInterval)
		}
	}()
}

func (s *Server) stopDraftImageMaintenance() {
	s.draftMaintMu.Lock()
	defer s.draftMaintMu.Unlock()
	if s.draftMaintCancel != nil {
		s.draftMaintCancel()
		s.draftMaintCancel = nil
	}
}
