package agent

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// AutoWindow 是静默自动升级允许「开始」的时间段：主机本地时间、24 小时制、精确到分钟，可以跨午夜（23:00-02:00）。
// 零值表示不限。时间段只约束「什么时候开始一次升级」；开始之后的下载与安装不会因为时间段结束而被放弃——
// 升级做到一半被打断，比稍微超出时间段更糟。
type AutoWindow struct {
	set        bool
	start, end int // 一天中的分钟数，[0, 1440)
}

const minutesPerDay = 24 * 60

// ParseAutoWindow 解析 HH:MM-HH:MM。空串是「不限」；起止相同的写法含义含糊（是整天还是一分钟都没有），一律拒绝。
func ParseAutoWindow(s string) (AutoWindow, error) {
	if s == "" {
		return AutoWindow{}, nil
	}
	if !termproto.ValidAutoWindow(s) {
		return AutoWindow{}, errors.New("时间段应写成 HH:MM-HH:MM（主机本地时间，24 小时制，起止不能相同），例如 03:00-05:00")
	}
	start := clockMinutes(s[:5])
	end := clockMinutes(s[6:])
	return AutoWindow{set: true, start: start, end: end}, nil
}

// clockMinutes 把 HH:MM（已经过 ValidAutoWindow 校验）转成一天中的分钟数。
func clockMinutes(hhmm string) int {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	return h*60 + m
}

// Always 表示不限时间。
func (w AutoWindow) Always() bool { return !w.set }

// String 还原成 HH:MM-HH:MM；不限时为空串。
func (w AutoWindow) String() string {
	if !w.set {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.start/60, w.start%60, w.end/60, w.end%60)
}

// Contains 判断 t（按它自己的时区取时钟）是否落在时间段内：起点含、终点不含。
func (w AutoWindow) Contains(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end // 跨午夜
}

// UntilOpen 返回距离时间段下一次开始还有多久；此刻已在时间段内（或不限）返回 0。
// 只按时钟读数推算，不处理夏令时跳变：调用方睡醒之后会重新判断，差一个小时也只是多睡一次。
func (w AutoWindow) UntilOpen(t time.Time) time.Duration {
	if w.Contains(t) {
		return 0
	}
	m := t.Hour()*60 + t.Minute()
	delta := (w.start - m + minutesPerDay) % minutesPerDay
	d := time.Duration(delta)*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond())
	if d < time.Second {
		d = time.Second
	}
	return d
}
