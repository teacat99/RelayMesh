// Package confirm 放确认项（ConfirmItem）的纯函数：引用解析、选项指纹、决策卡转换、投递给智能体的文案。
// 不依赖存储层，便于表驱动测试；存储与 REST / MCP 在 store、api、mcp 包里。
package confirm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// refAsIDThreshold：纯数字引用小于等于它时当作工作流内序号（Q12 的 12），更大的当数据库 id。
// 序号在单个工作流里不可能增长到这个量级，所以这个区分是无歧义的。
const refAsIDThreshold = 999999

// ParseRef 解析 Q12 / q12 / 12 / 数据库 id。
func ParseRef(ref string) (seq int, id uint, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, 0, false
	}
	upper := strings.ToUpper(ref)
	if strings.HasPrefix(upper, "Q") {
		n, err := strconv.Atoi(strings.TrimPrefix(upper, "Q"))
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return n, 0, true
	}
	n, err := strconv.Atoi(ref)
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	if n <= refAsIDThreshold {
		return n, 0, true
	}
	return 0, uint(n), true
}

// OptionsFingerprint 对选项列表做稳定哈希，用于检测文档决策卡的选项是否变化。
// 按选项 id 排序后再哈希：只是调换了选项顺序不算「选项变了」；增删选项、改文案、改推荐标记都算。
func OptionsFingerprint(opts []model.ConfirmOption) string {
	type row struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Detail      string `json:"detail,omitempty"`
		Recommended bool   `json:"recommended,omitempty"`
	}
	rows := make([]row, len(opts))
	for i, o := range opts {
		rows[i] = row{ID: o.ID, Label: o.Label, Detail: o.Detail, Recommended: o.Recommended}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// FromDecisionCard 把文档决策卡转成确认项字段：选项、默认选项、是否多选、是否允许备注、风险等级。
// 决策卡没写 default 时，用带推荐标记的第一个选项兜底，这样「采用推荐」与默认值不会各说各话。
func FromDecisionCard(card docs.DecisionCard) ([]model.ConfirmOption, string, bool, bool, string) {
	opts := make([]model.ConfirmOption, len(card.Options))
	for i, o := range card.Options {
		opts[i] = model.ConfirmOption{ID: o.ID, Label: o.Label, Detail: o.Detail, Recommended: o.Recommended}
	}
	risk := model.ConfirmRiskL1
	if card.Risk != "" {
		risk = model.NormalizeConfirmRisk(card.Risk)
	}
	def := card.Default
	if def == "" {
		for _, o := range opts {
			if o.Recommended {
				def = o.ID
				break
			}
		}
	}
	return opts, def, card.Multi, card.Note, risk
}

// RecommendedOptionIDs 返回带推荐标记的选项 id：单选取第一个，多选取全部；没有推荐时返回 nil。
func RecommendedOptionIDs(opts []model.ConfirmOption, multi bool) []string {
	var ids []string
	for _, o := range opts {
		if !o.Recommended {
			continue
		}
		ids = append(ids, o.ID)
		if !multi {
			break
		}
	}
	return ids
}

const answerTitleRunes = 60

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// optionLabelOf 在选项里按 id 找文案；找不到就回退到 id。
func optionLabelOf(opts []model.ConfirmOption, id string) string {
	for _, o := range opts {
		if o.ID == id {
			return o.Label
		}
	}
	return ""
}

// FormatAnswerLine 生成投递到收件箱的单条答复文案。
// 智能体可能在很久之后才读到，所以不能只写「Q12 选 a」：带上标题与所选项的文案，读到就能行动。
//
// 用户写了自定义答复（#183，预设选项都不合适时的出路）时，它永远是最后一段（可能多行，原样保留），
// 前面依次是所选项（单选时没有）、备注。计划确认（kind=plan）上的自定义答复不等于批准，文案里直接写明，
// 免得智能体把它当成放行：它是修改意见，批准之前仍只做只读的准备。
func FormatAnswerLine(item *model.ConfirmItem, optionIDs []string, note, custom string) string {
	view := model.NewConfirmItemView(item)
	parts := make([]string, 0, len(optionIDs))
	for _, id := range optionIDs {
		if label := optionLabelOf(view.Options, id); label != "" && label != id {
			parts = append(parts, fmt.Sprintf("%s（%s）", id, clipRunes(label, 40)))
		} else {
			parts = append(parts, id)
		}
	}
	custom = strings.TrimSpace(custom)
	line := fmt.Sprintf("%s「%s」已答复：", model.ConfirmRef(item.Seq), clipRunes(item.Title, answerTitleRunes))
	if len(parts) == 0 && custom != "" {
		line += "没有选预设选项"
	} else {
		line += "选 " + strings.Join(parts, "、")
	}
	if custom != "" && item.Kind == model.ConfirmKindPlan {
		line += "（自定义答复不等于批准：请当作修改意见，批准之前仍只做只读的准备）"
	}
	if n := strings.TrimSpace(note); n != "" {
		line += "；备注：" + n
	}
	if custom != "" {
		line += "；自定义答复：" + custom
	}
	return line
}

// FormatDismissLine 生成「用户放弃这个确认项」的收件箱文案。
func FormatDismissLine(item *model.ConfirmItem, reason string) string {
	line := fmt.Sprintf("%s「%s」已被用户忽略（不需要你再等它的答复）", model.ConfirmRef(item.Seq), clipRunes(item.Title, answerTitleRunes))
	if r := strings.TrimSpace(reason); r != "" {
		line += "：" + r
	}
	return line
}

// ExportHash 是导出清单里用来做「已知哈希」差量比较的哈希。
// 文档里的决策结果会随作答而变，但文档正文的哈希不变；如果只用正文哈希，智能体带着旧哈希来要差量时，
// 就会漏掉「决策刚被作答」的文档。所以有决策行的文档，把决策结果并进哈希。
func ExportHash(docHash string, rows []docs.DecisionExportRow) string {
	if len(rows) == 0 {
		return docHash
	}
	h := sha256.New()
	for _, r := range rows {
		h.Write([]byte(r.ID + "\x00" + r.Status + "\x00" + r.Ref + "\x00" + r.Result + "\n"))
	}
	return docHash + "+" + hex.EncodeToString(h.Sum(nil))[:12]
}
