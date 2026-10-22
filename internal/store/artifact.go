package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

var artifactKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)

// ArtifactDataSetInput 是写一个键的入参。ExpectedVersion 非空时做乐观并发：版本不符返回冲突（0 = 期望这个键还不存在）。
type ArtifactDataSetInput struct {
	DocID           uint
	Key             string
	ValueJSON       string
	ExpectedVersion *int
	ActorLabel      string
	ActorRef        string
}

func normalizeArtifactKey(key string) (string, error) {
	k := strings.TrimSpace(key)
	if !artifactKeyRe.MatchString(k) {
		return "", NewInvalidInputError("键名只能是字母、数字和 _ . : - ，长度 1~128")
	}
	return k, nil
}

// ArtifactDataList 列出一篇文档的全部键，按键名排序。
func (s *Store) ArtifactDataList(ctx context.Context, docID uint) ([]model.ArtifactData, error) {
	var rows []model.ArtifactData
	err := s.db.WithContext(ctx).Where("doc_id = ?", docID).Order("data_key ASC").Find(&rows).Error
	return rows, err
}

// ArtifactDataGet 读一个键；不存在返回 NotFound。
func (s *Store) ArtifactDataGet(ctx context.Context, docID uint, key string) (*model.ArtifactData, error) {
	k, err := normalizeArtifactKey(key)
	if err != nil {
		return nil, err
	}
	var row model.ArtifactData
	if err := s.db.WithContext(ctx).Where("doc_id = ? AND data_key = ?", docID, k).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("没有这个键：" + k)
		}
		return nil, err
	}
	return &row, nil
}

// ArtifactDataSet 写一个键（不存在则新建），校验 JSON 合法、总量与键数上限。
func (s *Store) ArtifactDataSet(ctx context.Context, in ArtifactDataSetInput) (*model.ArtifactData, error) {
	k, err := normalizeArtifactKey(in.Key)
	if err != nil {
		return nil, err
	}
	val := strings.TrimSpace(in.ValueJSON)
	if val == "" || !json.Valid([]byte(val)) {
		return nil, NewInvalidInputError("值必须是合法的 JSON")
	}
	if len(val) > model.ArtifactDataMaxBytes {
		return nil, NewInvalidInputError("单个值超过 256 KB")
	}
	var out model.ArtifactData
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur model.ArtifactData
		e := tx.Where("doc_id = ? AND data_key = ?", in.DocID, k).Take(&cur).Error
		exists := e == nil
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if in.ExpectedVersion != nil {
			have := 0
			if exists {
				have = cur.Version
			}
			if *in.ExpectedVersion != have {
				return NewConflictError("键「"+k+"」已被别人更新，请重新读取后再写", int64(have))
			}
		}
		// 总量与键数：把这次写入之后的状态算出来再比。
		var keys int64
		var bytes int64
		if err := tx.Model(&model.ArtifactData{}).Where("doc_id = ?", in.DocID).Count(&keys).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ArtifactData{}).Where("doc_id = ?", in.DocID).Select("COALESCE(SUM(LENGTH(value)), 0)").Scan(&bytes).Error; err != nil {
			return err
		}
		if !exists {
			keys++
		} else {
			bytes -= int64(len(cur.Value))
		}
		bytes += int64(len(val))
		if keys > model.ArtifactDataMaxKeys {
			return NewInvalidInputError("键数超过上限 200")
		}
		if bytes > model.ArtifactDataMaxBytes {
			return NewInvalidInputError("这篇文档的数据总量超过 256 KB")
		}
		now := time.Now()
		if exists {
			cur.Value, cur.Version, cur.UpdatedBy, cur.UpdatedByRef, cur.UpdatedAt = val, cur.Version+1, in.ActorLabel, in.ActorRef, now
			out = cur
			return tx.Save(&out).Error
		}
		out = model.ArtifactData{DocID: in.DocID, Key: k, Value: val, Version: 1, UpdatedBy: in.ActorLabel, UpdatedByRef: in.ActorRef, UpdatedAt: now, CreatedAt: now}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	if out.ID == 0 {
		// 并发创建同一个键时撞了唯一键：让调用方重试。
		return nil, NewConflictError("键「"+k+"」刚被别人创建，请重新读取后再写", 0)
	}
	return &out, nil
}

// ArtifactDataDelete 删除一个键；返回是否真的删了。
func (s *Store) ArtifactDataDelete(ctx context.Context, docID uint, key string) (bool, error) {
	k, err := normalizeArtifactKey(key)
	if err != nil {
		return false, err
	}
	res := s.db.WithContext(ctx).Where("doc_id = ? AND data_key = ?", docID, k).Delete(&model.ArtifactData{})
	return res.RowsAffected > 0, res.Error
}

// ArtifactCommentInput 是新增评论的入参。
type ArtifactCommentInput struct {
	DocID       uint
	Anchor      string
	Body        string
	AuthorKind  string
	AuthorRef   string
	AuthorLabel string
}

// AddArtifactComment 新增一条评论。
func (s *Store) AddArtifactComment(ctx context.Context, in ArtifactCommentInput) (*model.ArtifactComment, error) {
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return nil, NewInvalidInputError("请填写评论内容")
	}
	if utf8.RuneCountInString(body) > model.ArtifactCommentMaxLen {
		return nil, NewInvalidInputError("评论过长（上限 4000 字）")
	}
	anchor := strings.TrimSpace(in.Anchor)
	if utf8.RuneCountInString(anchor) > 128 {
		return nil, NewInvalidInputError("锚点过长")
	}
	c := model.ArtifactComment{DocID: in.DocID, Anchor: anchor, Body: body, AuthorKind: in.AuthorKind, AuthorRef: in.AuthorRef, AuthorLabel: in.AuthorLabel, CreatedAt: time.Now()}
	if c.AuthorKind == "" {
		c.AuthorKind = "human"
	}
	if err := s.db.WithContext(ctx).Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListArtifactComments 按时间正序列出评论；默认只含未解决的。
func (s *Store) ListArtifactComments(ctx context.Context, docID uint, includeResolved bool, limit int) ([]model.ArtifactComment, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Where("doc_id = ?", docID)
	if !includeResolved {
		q = q.Where("resolved = ?", false)
	}
	var rows []model.ArtifactComment
	err := q.Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ResolveArtifactComment 把评论标成已解决 / 重新打开。
func (s *Store) ResolveArtifactComment(ctx context.Context, docID, commentID uint, resolved bool) (*model.ArtifactComment, error) {
	var c model.ArtifactComment
	if err := s.db.WithContext(ctx).Where("id = ? AND doc_id = ?", commentID, docID).Take(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("没有这条评论")
		}
		return nil, err
	}
	c.Resolved = resolved
	if resolved {
		now := time.Now()
		c.ResolvedAt = &now
	} else {
		c.ResolvedAt = nil
	}
	if err := s.db.WithContext(ctx).Save(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// CountOpenArtifactComments 是未解决评论数（文档列表角标用）。
func (s *Store) CountOpenArtifactComments(ctx context.Context, docID uint) int {
	var n int64
	_ = s.db.WithContext(ctx).Model(&model.ArtifactComment{}).Where("doc_id = ? AND resolved = ?", docID, false).Count(&n).Error
	return int(n)
}
