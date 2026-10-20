package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 草稿图片（云存储）的配额。图片正文是 Base64 文本，所以限额都按文本长度计。
const (
	// MaxDraftImageChars 单张草稿图片正文的长度上限（约 30MB 的原图）。
	MaxDraftImageChars = 40 << 20
	// MaxDraftImagesPerWorkflow 一个工作流同时能存放的草稿图片张数。
	MaxDraftImagesPerWorkflow = 60
	// MaxDraftImageCharsPerWorkflow 一个工作流所有草稿图片正文的总长度上限。
	MaxDraftImageCharsPerWorkflow = 160 << 20
)

// DraftImageInfo 是一张草稿图片的元数据（不含正文）。
type DraftImageInfo struct {
	Key       string    `json:"key"`
	Name      string    `json:"name,omitempty"`
	Format    string    `json:"format,omitempty"`
	Hash      string    `json:"hash,omitempty"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PutDraftImage 保存（或重新确认）一张草稿图片：同一工作流里相同的内容键只存一份，重复上传是幂等的，只刷新写入时间。
// 超过单张 / 张数 / 总量配额时返回 invalid_input，不写入任何内容。
func (s *Store) PutDraftImage(ctx context.Context, img model.DraftImage) error {
	img.WorkflowID = strings.TrimSpace(img.WorkflowID)
	if img.WorkflowID == "" || img.Key == "" {
		return NewInvalidInputError("缺少工作流或图片键")
	}
	if img.Data == "" {
		return NewInvalidInputError("图片内容为空")
	}
	if len(img.Data) > MaxDraftImageChars {
		return NewInvalidInputError(fmt.Sprintf("单张草稿图片不能超过 %d MB", MaxDraftImageChars>>20))
	}
	img.Size = len(img.Data)
	img.UpdatedAt = time.Now()

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var others struct {
			Count int64
			Total int64
		}
		if err := tx.Model(&model.DraftImage{}).
			Select("COUNT(*) AS count, COALESCE(SUM(size), 0) AS total").
			Where("workflow_id = ? AND key <> ?", img.WorkflowID, img.Key).
			Scan(&others).Error; err != nil {
			return err
		}
		if others.Count >= MaxDraftImagesPerWorkflow {
			return NewInvalidInputError(fmt.Sprintf("这个工作流的草稿图片已达 %d 张上限，请先移除不用的图片", MaxDraftImagesPerWorkflow))
		}
		if others.Total+int64(img.Size) > MaxDraftImageCharsPerWorkflow {
			return NewInvalidInputError(fmt.Sprintf("这个工作流的草稿图片总量已超过 %d MB，请先移除不用的图片", MaxDraftImageCharsPerWorkflow>>20))
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "workflow_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "format", "data_type", "hash", "width", "height", "size", "data", "updated_at"}),
		}).Create(&img).Error
	})
}

// GetDraftImage 读取一张草稿图片（含正文）；不存在返回 nil, nil。
func (s *Store) GetDraftImage(ctx context.Context, workflowID, key string) (*model.DraftImage, error) {
	var img model.DraftImage
	err := s.db.WithContext(ctx).Where("workflow_id = ? AND key = ?", workflowID, key).First(&img).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &img, nil
}

// ListDraftImages 列出一个工作流的草稿图片元数据（不读出正文），按写入时间排序。
func (s *Store) ListDraftImages(ctx context.Context, workflowID string) ([]DraftImageInfo, error) {
	var list []DraftImageInfo
	err := s.db.WithContext(ctx).Model(&model.DraftImage{}).
		Select("key, name, format, hash, width, height, size, updated_at").
		Where("workflow_id = ?", workflowID).
		Order("updated_at ASC, key ASC").
		Scan(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

// ResolveDraftImages 按内容键取回一批草稿图片（含正文），缺失的键不会出现在结果里。
func (s *Store) ResolveDraftImages(ctx context.Context, workflowID string, keys []string) (map[string]model.DraftImage, error) {
	out := make(map[string]model.DraftImage, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	var rows []model.DraftImage
	if err := s.db.WithContext(ctx).Where("workflow_id = ? AND key IN ?", workflowID, keys).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Key] = r
	}
	return out, nil
}

// DeleteDraftImages 删除一个工作流里指定的草稿图片，返回实际删除的张数。
func (s *Store) DeleteDraftImages(ctx context.Context, workflowID string, keys []string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var n int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("workflow_id = ? AND key IN ?", workflowID, keys).Delete(&model.DraftImage{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// DeleteAllDraftImages 删除一个工作流的全部草稿图片（草稿被整体销毁时）。
func (s *Store) DeleteAllDraftImages(ctx context.Context, workflowID string) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("workflow_id = ?", workflowID).Delete(&model.DraftImage{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// DraftReferencedKeys 解析草稿 JSON，返回它引用的全部图片内容键（解析失败按「没有引用」处理）。
func DraftReferencedKeys(draftsJSON string) map[string]struct{} {
	refs := map[string]struct{}{}
	if strings.TrimSpace(draftsJSON) == "" {
		return refs
	}
	var doc struct {
		Drafts []struct {
			Images []struct {
				Key string `json:"key"`
			} `json:"images"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(draftsJSON), &doc); err != nil {
		return refs
	}
	for _, d := range doc.Drafts {
		for _, img := range d.Images {
			if img.Key != "" {
				refs[img.Key] = struct{}{}
			}
		}
	}
	return refs
}

// SweepDraftImages 清理遗弃的草稿图片：最近一次写入早于 olderThan，并且所在工作流已保存的草稿不再引用它
// （没有草稿记录也算不再引用）。这是客户端异常退出、草稿被另一台设备整体覆盖时的兜底，正常路径是客户端在草稿销毁时主动删除。
func (s *Store) SweepDraftImages(ctx context.Context, olderThan time.Time) (int64, error) {
	var stale []struct {
		WorkflowID string
		Key        string
	}
	if err := s.db.WithContext(ctx).Model(&model.DraftImage{}).
		Select("workflow_id, key").
		Where("updated_at < ?", olderThan).
		Scan(&stale).Error; err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	byWorkflow := map[string][]string{}
	for _, r := range stale {
		byWorkflow[r.WorkflowID] = append(byWorkflow[r.WorkflowID], r.Key)
	}
	var total int64
	for wid, keys := range byWorkflow {
		draft, err := s.GetWorkflowDraft(ctx, wid)
		if err != nil {
			return total, err
		}
		refs := map[string]struct{}{}
		if draft != nil {
			refs = DraftReferencedKeys(draft.DraftsJSON)
		}
		var drop []string
		for _, k := range keys {
			if _, ok := refs[k]; !ok {
				drop = append(drop, k)
			}
		}
		for start := 0; start < len(drop); start += 200 {
			end := start + 200
			if end > len(drop) {
				end = len(drop)
			}
			n, err := s.DeleteDraftImages(ctx, wid, drop[start:end])
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}
