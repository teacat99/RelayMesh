package oauth

import (
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 接入窗口（#239）：自动注册（DCR，POST /oauth/register）默认不是常开的。
// 管理员要先在「设置 → OAuth 授权」里开一个限时窗口，窗口期间客户端才能自己登记；窗口之外一律 403 registration_window_closed。
// 这样公网上就不再有一个常开的、任何人都能往库里写登记的入口。
//
// 语义与 ExecMesh 的发现窗口（terminal/registry.go）一致：默认 10 分钟、续期在剩余时间上累加、总剩余不超过 60 分钟、
// 只存内存（Hub 重启即关闭）。窗口只管 DCR；CIMD 客户端（client_id 本身是 https 地址）不登记，不受窗口约束。
const (
	// DefaultDCRWindow 是管理员打开接入窗口时的默认时长（也是「续期」每次加的时长）。
	DefaultDCRWindow = 10 * time.Minute
	// MaxDCRWindow 是接入窗口的时长上限：无论怎么续，总剩余都不超过它。
	MaxDCRWindow = 60 * time.Minute
)

// 自动注册的三种方式（见 model.OAuthPolicy 的说明）。
const (
	DCRModeOff    = "off"    // 关闭：POST /oauth/register 一律 403 registration_disabled
	DCRModeWindow = "window" // 限时窗口（默认）：窗口开着才受理
	DCRModeAlways = "always" // 常开：任何时候都受理
)

// DCRModeOf 把策略里的两个布尔值翻译成自动注册方式。
func DCRModeOf(p model.OAuthPolicy) string {
	switch {
	case !p.AllowDCR:
		return DCRModeOff
	case p.DCRAlways:
		return DCRModeAlways
	default:
		return DCRModeWindow
	}
}

// dcrWindow 是接入窗口的内存状态；零值就是「关闭」。
type dcrWindow struct {
	mu    sync.Mutex
	until time.Time
}

// clampDCRWindow 把请求的时长落到 (0, MaxDCRWindow]：没给（≤0）用默认值，过长的截到上限。
func clampDCRWindow(d time.Duration) time.Duration {
	if d <= 0 {
		d = DefaultDCRWindow
	}
	if d > MaxDCRWindow {
		d = MaxDCRWindow
	}
	return d
}

// open 打开窗口：到期时间重置为「从现在起 d」（剩余 9 分钟时再点「开启 10 分钟」只会多出 1 分钟，要累加请用 extend），返回到期时间。
func (w *dcrWindow) open(now time.Time, d time.Duration) time.Time {
	d = clampDCRWindow(d)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.until = now.Add(d)
	return w.until
}

// extend 在窗口开着时把到期时间再往后顺延 add（总剩余不超过 MaxDCRWindow），返回新的到期时间。
// 窗口没开着时返回 false，调用方应改走 open。
func (w *dcrWindow) extend(now time.Time, add time.Duration) (time.Time, bool) {
	add = clampDCRWindow(add)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !now.Before(w.until) {
		return time.Time{}, false
	}
	next := w.until.Add(add)
	if limit := now.Add(MaxDCRWindow); next.After(limit) {
		next = limit
	}
	w.until = next
	return next, true
}

// close 立即关闭窗口，返回此前是否开着。
func (w *dcrWindow) close(now time.Time) (wasOpen bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wasOpen = now.Before(w.until)
	w.until = time.Time{}
	return wasOpen
}

// state 返回窗口此刻是否开着与到期时间（没开着时 until 为零值）。
func (w *dcrWindow) state(now time.Time) (open bool, until time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if now.Before(w.until) {
		return true, w.until
	}
	return false, time.Time{}
}

// DCRStatus 是设置页「接入窗口」卡片用的只读视图：现算、不落库，只描述事实。
// 它挂在 EffectiveStatus.DCR 下面返回，与可编辑的策略分开——「策略里写着限时窗口」不等于「窗口此刻开着」。
type DCRStatus struct {
	// Mode：自动注册方式 off / window / always（由策略的 allow_dcr 与 dcr_always 决定）。
	Mode string `json:"mode"`
	// Open：此刻 POST /oauth/register 会不会被受理。授权服务器没在服务时一律 false；always 为 true；window 看窗口是否开着。
	Open bool `json:"open"`
	// Until 与 RemainingSeconds 只在 window 方式且窗口开着时有值。
	Until            *time.Time `json:"until,omitempty"`
	RemainingSeconds int        `json:"remaining_seconds"`
	// DefaultMinutes / MaxMinutes 是窗口的默认时长与上限（页面据此写按钮与说明，不在前端再写死一份）。
	DefaultMinutes int `json:"default_minutes"`
	MaxMinutes     int `json:"max_minutes"`
}

// BuildDCRStatus 由策略、此刻时间、窗口到期时间（零值 = 没开）与「授权服务器是否在服务」算出展示状态。
func BuildDCRStatus(p model.OAuthPolicy, now, windowUntil time.Time, serving bool) DCRStatus {
	st := DCRStatus{
		Mode:           DCRModeOf(p),
		DefaultMinutes: int(DefaultDCRWindow / time.Minute),
		MaxMinutes:     int(MaxDCRWindow / time.Minute),
	}
	if !serving {
		return st
	}
	switch st.Mode {
	case DCRModeAlways:
		st.Open = true
	case DCRModeWindow:
		if now.Before(windowUntil) {
			st.Open = true
			u := windowUntil
			st.Until = &u
			if rem := int(windowUntil.Sub(now).Seconds()); rem > 0 {
				st.RemainingSeconds = rem
			}
		}
	}
	return st
}

// DCRStatus 返回策略 p 下接入窗口此刻的展示状态。
func (s *Service) DCRStatus(p model.OAuthPolicy) DCRStatus {
	now := s.now()
	_, until := s.dcrWin.state(now)
	return BuildDCRStatus(p, now, until, s.Enabled())
}

// dcrAccepting 报告此刻 POST /oauth/register 是否应受理（不含速率限制）：策略允许，且是常开或窗口开着。
// 返回 reason：不受理时是对应的错误码（registration_disabled / registration_window_closed）。
func (s *Service) dcrAccepting(p model.OAuthPolicy) (ok bool, reason string) {
	switch DCRModeOf(p) {
	case DCRModeOff:
		return false, "registration_disabled"
	case DCRModeAlways:
		return true, ""
	}
	if open, _ := s.dcrWin.state(s.now()); open {
		return true, ""
	}
	return false, "registration_window_closed"
}

// OpenDCRWindow 打开接入窗口（extend=false：把剩余时间重置为 d）或续期（extend=true：在剩余时间上累加 d，窗口没开着时等同于打开）。
// d 小于等于 0 用默认 10 分钟，超过 60 分钟按 60 分钟。返回新的到期时间。
// 前置条件由调用方（API 层）检查：授权服务器在服务、自动注册方式是限时窗口。
func (s *Service) OpenDCRWindow(d time.Duration, extend bool) time.Time {
	now := s.now()
	if extend {
		if until, ok := s.dcrWin.extend(now, d); ok {
			return until
		}
	}
	return s.dcrWin.open(now, d)
}

// CloseDCRWindow 立即关闭接入窗口，返回此前是否开着。
func (s *Service) CloseDCRWindow() bool {
	return s.dcrWin.close(s.now())
}
