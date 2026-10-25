package ntfy

import (
	"fmt"
	"strings"
	"time"
)

// 「异常」推送（#235）：Agent 出错或被中断、Agent 疑似卡住、ExecMesh 主机掉线、安全告警。
//
// 它们和「需要你处理」的消息不同：没有人在等你回复，是系统发现「哪里不对劲了」告诉你一声。
// 四类默认都不推送（见 priority.go 里的内置默认），在设置页「各类消息的优先级」里逐类打开之后才会有推送。
// 推送内容只有名字、主机显示名与一句原因——从不带终端内容、命令、IP 与任何凭据（和守护、运行结束推送一致）。
//
// 分工：「什么时候算异常、同一段异常只报一次」由发现它的地方负责（api 层的巡检器与审计 / 登录锁定挂钩——
// 它们手里有「这一段异常是否已经报过」的状态）；这里只做三件事：这一类开没开、去重（同一个 Key 10 分钟内一次，
// 期间重复发生的次数合并进下一条）、全局限流。异常不经过统一通知闸：闸管的是免打扰、会话静音与浏览器通道，
// 这三样对「穿透、且只走 ntfy」的异常都不起作用，开关就是表里的「不推送」。

// 异常的细分（ExceptionInfo.Sub）：同一类里更具体的情形，决定标题怎么写。
const (
	SubAgentError   = "error"   // Agent 出错结束
	SubAgentAborted = "aborted" // Agent 被中断

	SubOAuthCodeReplay    = "oauth_code_replay"    // OAuth 授权码被重放
	SubOAuthRefreshReplay = "oauth_refresh_replay" // OAuth 刷新令牌被重放
	SubUnlockLocked       = "unlock_locked"        // 终端解锁尝试过多，被临时锁定
	SubLoginLocked        = "login_locked"         // 登录尝试过多，来源被临时锁定
)

// maxExceptionRepeats 是同时记着「被去重挡掉的重复次数」的 Key 数上限：来源可以被攻击者变换，不能无界增长。
const maxExceptionRepeats = 200

// ExceptionInfo 是一条异常推送要用到的字段。纯数据：由接线处（api）翻译好交来，ntfy 因此仍只依赖标准库与 model。
type ExceptionInfo struct {
	// Kind 是表里的异常类型：KindAgentError / KindAgentStalled / KindHostOffline / KindSecurityAlert。
	Kind string
	// Sub 是细分，见上面的 Sub* 常量；可空。
	Sub string
	// Key 标识「同一件事」：同一个 Key 在去重窗口（10 分钟）内只推一次，期间重复发生的次数合并进下一条。
	Key string
	// Name 是对象的名字：Agent 所在项目的名字、主机的显示名；安全告警留空。
	Name string
	// Host 是 Agent 所在主机的显示名；没解析出来就留空——推送里宁可少一行，也不拿机器码凑数。
	Host string
	// Reason 是一句人话的原因；调用方保证里面没有终端内容、命令与任何凭据。
	Reason string
	// Since 是异常的起点（卡住 / 掉线开始的时间、出错发生的时间）；零值表示不详。
	Since time.Time
	// DeepLink 是站内路径（以 / 开头），拼到「点击打开的地址」后面；可空。
	DeepLink string
}

// NotifyException 登记一条异常。立即返回；事件当场复制。
// 这一类没打开时会在队列另一头被静默丢掉，调用方不必（也不该）自己判断开关。
func (n *Notifier) NotifyException(in ExceptionInfo) {
	if n == nil || in.Key == "" || !isExceptionKind(in.Kind) {
		return
	}
	n.submit(job{exception: &in})
}

// isExceptionKind 报告 kind 是不是表里「异常」组的类型。
func isExceptionKind(kind string) bool {
	k, ok := kindIndex[kind]
	return ok && k.Group == GroupException
}

// ExceptionKindEnabled 报告配置里有没有任何一类异常被打开。巡检器据此决定要不要白白扫一遍：
// 没打开时一次库都不查。
func (c Config) ExceptionKindEnabled() bool {
	if !c.Enabled {
		return false
	}
	for _, k := range kindTable {
		if k.Group == GroupException && c.kindEnabled(k.ID) {
			return true
		}
	}
	return false
}

// handleException 处理一条异常：这一类开着才继续；同一个 Key 10 分钟内只推一次（重复的只计数）；限流；发送。
func (n *Notifier) handleException(cfg Config, in ExceptionInfo) {
	if !cfg.kindEnabled(in.Kind) {
		return
	}
	if !n.firstSight("exc:" + in.Key) {
		n.countExceptionRepeat(in.Key)
		return
	}
	merged, ok := n.allow()
	if !ok {
		return
	}
	repeats := n.takeExceptionRepeats(in.Key)
	n.deliver(cfg, BuildExceptionMessage(cfg, in, repeats, merged, n.now()), merged)
}

// countExceptionRepeat 记下一次被去重挡掉的重复事件；下一条同 Key 的推送会说明「期间又发生了几次」。
func (n *Notifier) countExceptionRepeat(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, known := n.excRepeats[key]; !known && len(n.excRepeats) >= maxExceptionRepeats {
		n.excRepeats = make(map[string]int)
	}
	n.excRepeats[key]++
}

// takeExceptionRepeats 取走并清零这个 Key 积累的重复次数。
func (n *Notifier) takeExceptionRepeats(key string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.excRepeats[key]
	delete(n.excRepeats, key)
	return c
}

// BuildExceptionMessage 生成异常的推送内容。repeats 是上一条同类推送之后又发生的次数；
// merged 是此前因全局限流而没发出去的通知条数；now 用来把起点写成「已持续多久」。
func BuildExceptionMessage(cfg Config, in ExceptionInfo, repeats, merged int, now time.Time) Message {
	title, tag := exceptionTitle(in)
	lines := make([]string, 0, 6)

	if r := oneLine(in.Reason); r != "" {
		lines = append(lines, truncateRunes(r, reasonMaxLen))
	}
	if h := oneLine(in.Host); h != "" {
		lines = append(lines, "主机："+truncateRunes(h, hostNameMaxLen))
	}
	if !in.Since.IsZero() && now.After(in.Since) {
		switch in.Kind {
		case KindAgentError:
			lines = append(lines, formatDuration(now.Sub(in.Since))+"前发生")
		case KindSecurityAlert:
			// 安全告警是一个时间点，不是一段持续的状态：不写「已持续」。
		default:
			lines = append(lines, "已持续 "+formatDuration(now.Sub(in.Since)))
		}
	}
	if tip := exceptionTip(in.Kind); tip != "" {
		lines = append(lines, tip)
	}
	if repeats > 0 {
		lines = append(lines, fmt.Sprintf("（上一条之后，同类事件又发生了 %d 次，已合并）", repeats))
	}
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}

	return Message{
		Title:    title,
		Body:     strings.Join(lines, "\n"),
		Priority: cfg.msgPriority(in.Kind),
		Tags:     []string{tag},
		Click:    eventClick(cfg.ClickURL, in.DeepLink),
	}
}

// exceptionTitle 给出标题与 ntfy 标签（标签在手机上显示成表情）。名字缺失时写不带名字的说法，不拿占位词凑数。
func exceptionTitle(in ExceptionInfo) (title, tag string) {
	name := truncateRunes(oneLine(in.Name), nameMaxLen)
	subject := "Agent"
	if name != "" {
		subject = fmt.Sprintf("「%s」的 Agent", name)
	}
	switch in.Kind {
	case KindAgentError:
		if in.Sub == SubAgentAborted {
			return subject + " 被中断", "warning"
		}
		return subject + " 出错结束", "rotating_light"
	case KindAgentStalled:
		return subject + " 疑似卡住", "hourglass_flowing_sand"
	case KindHostOffline:
		if name == "" {
			return "ExecMesh 主机已掉线", "electric_plug"
		}
		return fmt.Sprintf("主机「%s」已掉线", name), "electric_plug"
	case KindSecurityAlert:
		switch in.Sub {
		case SubOAuthCodeReplay:
			return "安全告警：OAuth 授权码被重放", "rotating_light"
		case SubOAuthRefreshReplay:
			return "安全告警：OAuth 刷新令牌被重放", "rotating_light"
		case SubUnlockLocked:
			return "安全告警：终端解锁尝试过多", "lock"
		case SubLoginLocked:
			return "安全告警：登录尝试过多", "lock"
		}
		return "安全告警", "rotating_light"
	}
	return KindLabel(in.Kind), "warning"
}

// exceptionTip 是正文末尾告诉你「去哪看」的一句话。
func exceptionTip(kind string) string {
	switch kind {
	case KindAgentError, KindAgentStalled:
		return "到控制台的 Agent 状态里看看这个对话。"
	case KindHostOffline:
		return "到终端页的主机列表里看看它的状态。"
	case KindSecurityAlert:
		return "详情见控制台的审计日志。"
	}
	return ""
}
