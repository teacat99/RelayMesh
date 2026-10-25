package terminal

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端启动器预设（会话页顶栏的「终端」入口）。预设只是「在哪台主机、哪个目录、自动输入什么命令」这份配置，
// 真正执行靠的是人在界面里点「启动」——新建会话与连接都需要解锁。但配置本身会决定人点下去之后会运行什么，
// 所以修改预设和执行一样要求刚刚解锁：只拿到登录态（没有密码）的人，不能先把预设改成别的命令、再等人去点。
// 命令内容不写进审计：命令里可能带着令牌或密码。

type launcherRequest struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Dir     string `json:"dir"`
	Command string `json:"command"`
	// Guard 是会话守护配置（D-214）；不带（或为 null）表示这个预设不用守护。
	Guard *store.LauncherGuard `json:"guard"`
	// Kind 是预设类型：不带（或空）= 开终端；"headless" = 非交互续聊（#126），运行配置在 Run 里。
	Kind string             `json:"kind"`
	Run  *store.HeadlessRun `json:"run"`
}

// launcherInteractiveRetiredMsg 是拒绝保存交互式预设时给界面的说明。
const launcherInteractiveRetiredMsg = "交互式「开终端」预设已停用：现在只能新建「非交互续聊」预设；已有的旧预设只能删除。要在主机上开终端，请直接用 ExecMesh 的网页终端。"

// isInteractiveLauncherKind 判断预设类型是不是交互式「开终端」：不带类型（旧存档都是这种）或写成 terminal。
func isInteractiveLauncherKind(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	return k == "" || k == store.LauncherKindTerminal
}

// handleLauncherSave 新增或覆盖一条预设（编号在路径里，由界面生成）。
//
// 交互式「开终端」预设已停用（#245 第一期）：新建与修改都拒绝，旧预设只能删除。
// 这条规则只放在这个写入口，不放进 store 的 Validate：Validate 同时用于读取（不合法的条目读取时会被丢掉），
// 放进去会让旧存档里的交互式预设在读取时凭空消失——那是第三期的事，要先导出备份、另发确认。
func (h *Hub) handleLauncherSave(c *gin.Context) {
	var req launcherRequest
	if !bindJSON(c, &req) {
		return
	}
	if isInteractiveLauncherKind(req.Kind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": launcherInteractiveRetiredMsg, "code": "launcher_interactive_retired"})
		return
	}
	// 主机码按规范形式保存（大小写、连字符不敏感）；格式不对的原样交给校验去报错。
	if n, err := termproto.NormalizeMachineCode(req.Host); err == nil {
		req.Host = n
	}
	ctx := c.Request.Context()
	l := store.TerminalLauncher{ID: c.Param("id"), Name: req.Name, Host: req.Host, Dir: req.Dir, Command: req.Command, Guard: req.Guard, Kind: req.Kind, Run: req.Run}
	_, created, err := h.store.UpsertTerminalLauncher(ctx, l)
	if err != nil {
		h.failLauncher(c, err)
		return
	}
	detail := "update id=" + l.ID
	if created {
		detail = "new id=" + l.ID
	}
	h.audit.record(ctx, auditEvent{Event: EvLauncherSave, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: l.Host, Detail: detail})
	c.JSON(http.StatusOK, gin.H{"prefs": h.prefsView(ctx)})
}

// handleLauncherDelete 按编号删除一条预设；它已经不在了（别的浏览器删过）返回 404，界面按「已经没有了」处理。
func (h *Hub) handleLauncherDelete(c *gin.Context) {
	ctx := c.Request.Context()
	_, gone, err := h.store.DeleteTerminalLauncher(ctx, c.Param("id"))
	if err != nil {
		h.failLauncher(c, err)
		return
	}
	h.audit.record(ctx, auditEvent{Event: EvLauncherDelete, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: gone.Host, Detail: "id=" + gone.ID})
	c.JSON(http.StatusOK, gin.H{"prefs": h.prefsView(ctx)})
}

func (h *Hub) failLauncher(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidLauncher):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_launcher"})
	case errors.Is(err, store.ErrLauncherLimit):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "launcher_limit"})
	case errors.Is(err, store.ErrLauncherNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "这个预设已经不存在了（可能在别处删掉了）", "code": "launcher_not_found"})
	default:
		log.Printf("[terminal] 保存终端启动预设失败: %v", err)
		h.fail(c, err)
	}
}
