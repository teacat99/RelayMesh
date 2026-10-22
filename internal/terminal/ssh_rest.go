package terminal

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// SSH 主机的管理路由。全部要求「解锁」：登记 / 修改连接信息会让 Hub 能够以保存的凭据登录远端主机，
// 与启用 Agent 主机同等敏感。凭据只出现在请求体里：不回显、不写日志、不进审计。
func (h *Hub) mountSSH(unlocked *gin.RouterGroup) {
	unlocked.POST("/ssh/probe", h.handleSSHProbe)
	unlocked.POST("/ssh/hosts", h.handleSSHAdd)
	unlocked.POST("/hosts/:code/ssh/test", h.handleSSHTest)
	unlocked.POST("/hosts/:code/ssh/probe", h.handleSSHProbeHost)
	unlocked.POST("/hosts/:code/ssh/connection", h.handleSSHConn)
	unlocked.POST("/hosts/:code/ssh/rekey", h.handleSSHRekey)
}

// bindSSHJSON 与 bindJSON 相同，但允许更大的请求体：私钥文本最大 16 KiB，转义后会超过 bindJSON 的上限。
func bindSSHJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 40<<10)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确", "code": "bad_request"})
		return false
	}
	return true
}

// sshFail 把 SSHError 写成统一的错误响应；其余错误交给通用处理。
func (h *Hub) sshFail(c *gin.Context, err error) {
	var se *SSHError
	if errors.As(err, &se) {
		c.JSON(se.Status, gin.H{"error": se.Message, "code": se.Code})
		return
	}
	h.fail(c, err)
}

type sshProbeRequest struct {
	Address string `json:"address"`
	// Gateway 非空表示由这台网关主机（机器码）出站探测；空串由 Hub 直接探测。
	Gateway string `json:"gateway"`
}

func (h *Hub) handleSSHProbe(c *gin.Context) {
	var req sshProbeRequest
	if !bindJSON(c, &req) {
		return
	}
	res, err := h.SSHProbeVia(c.Request.Context(), req.Address, req.Gateway, subjectOf(c), c.ClientIP())
	if err != nil {
		h.sshFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"probe": res})
}

func (h *Hub) handleSSHAdd(c *gin.Context) {
	var req SSHAddRequest
	if !bindSSHJSON(c, &req) {
		return
	}
	view, grant, err := h.SSHAdd(c.Request.Context(), req, subjectOf(c), c.ClientIP())
	// 请求体里的凭据不再需要：尽早丢弃引用，缩短它们留在内存里的时间。
	req.Password, req.PrivateKey, req.Passphrase = "", "", ""
	if err != nil {
		h.sshFail(c, err)
		return
	}
	resp := gin.H{"host": view}
	if grant != nil {
		resp["grant"] = grant
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Hub) handleSSHTest(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	res, err := h.SSHTest(c.Request.Context(), code, subjectOf(c), c.ClientIP())
	if err != nil {
		h.sshFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": res})
}

func (h *Hub) handleSSHProbeHost(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	res, err := h.SSHProbeHost(c.Request.Context(), code, subjectOf(c), c.ClientIP())
	if err != nil {
		h.sshFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"probe": res})
}

func (h *Hub) handleSSHConn(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req SSHConnUpdateRequest
	if !bindSSHJSON(c, &req) {
		return
	}
	view, err := h.SSHUpdateConn(c.Request.Context(), code, req, subjectOf(c), c.ClientIP())
	req.Password, req.PrivateKey, req.Passphrase = "", "", ""
	if err != nil {
		h.sshFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": view})
}

type sshRekeyRequest struct {
	Fingerprint string `json:"fingerprint"`
}

func (h *Hub) handleSSHRekey(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req sshRekeyRequest
	if !bindJSON(c, &req) {
		return
	}
	view, err := h.SSHRekey(c.Request.Context(), code, req.Fingerprint, subjectOf(c), c.ClientIP())
	if err != nil {
		h.sshFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": view})
}
