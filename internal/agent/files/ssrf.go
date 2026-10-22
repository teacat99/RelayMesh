package files

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// push_url 让 Agent 代替模型去访问一个网址，这是典型的 SSRF 入口：模型可以借 Agent 的网络位置探测内网、
// 访问云厂商的元数据服务（169.254.169.254）或本机上的管理接口。防护的核心有三条：
//
//  1. 校验发生在「真正建立连接」的那一刻：先解析出全部地址，只要有一个不是公网单播地址就整体拒绝（防止混合应答），
//     然后直接拨号到已校验的 IP，而不是再交给系统按主机名解析一次（防止 DNS 重绑定）；
//     拨号的 Control 钩子再校验一次实际连接的地址（纵深防御）。
//  2. 重定向的每一跳都走同一个传输层，所以每一跳都会重新经过上面的校验；协议与凭据在每一跳都检查，跳数有上限。
//  3. 不使用任何代理（环境变量里的代理会让目标主机名由代理解析，校验就被绕过了），不解压（避免压缩炸弹）。
//
// 「无法解析」与「指向非公网」返回同一个错误，避免模型借错误差异枚举内网主机名。

// blockedPrefixes 是不属于公网单播的地址段（含各类保留 / 文档 / 转换前缀）。
var blockedPrefixes = mustPrefixes(
	// IPv4
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	// IPv6：未指定 / 回环 / 兼容地址、IPv4 映射、NAT64、丢弃、IETF 协议分配（含 Teredo）、文档、6to4、唯一本地、链路本地、站点本地、组播
	"::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
	"2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// publicAddr 判断地址是否可以被 push_url 访问：必须是公网单播地址。
func publicAddr(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" { // 带区域标识的只可能是链路本地等本地范围地址
		return false
	}
	a = a.Unmap()
	if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// URLPolicy 是 push_url 的网络策略。零值是默认的严格策略。
type URLPolicy struct {
	// AllowPrivate 放开对回环 / 私网 / 链路本地地址的限制。只能由主机的拥有者在本机启动参数里打开，服务端无法设置。
	AllowPrivate bool
	// Lookup 解析主机名（测试可替换）；nil 使用系统解析器。
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial 建立到「已校验的 IP:端口」的连接（测试可替换）；nil 使用带 Control 校验的真实拨号。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// errBlocked 表示目标地址或网址不被允许；对外统一呈现为 url_blocked。
var errBlocked = errors.New("url blocked")

const (
	maxRedirects   = 5
	dialTimeout    = 10 * time.Second
	headerTimeout  = 20 * time.Second
	tlsTimeout     = 10 * time.Second
	maxHeaderBytes = 64 << 10
	blockedMsg     = "无法访问该网址：地址无法解析，或不是公网地址（回环 / 私网 / 链路本地等一律拒绝）"
	badURLMsg      = "网址只支持 http 与 https，且不能带用户名口令"
)

// checkURL 检查网址的形状：协议、凭据、主机、端口。IP 字面量在这里先做一次快速校验（给出明确错误）；
// 主机名的地址校验发生在拨号时。
func (p URLPolicy) checkURL(u *url.URL) *Error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail(termproto.FileErrURLBlocked, badURLMsg)
	}
	if u.User != nil || u.Opaque != "" {
		return fail(termproto.FileErrURLBlocked, badURLMsg)
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, " \t\r\n\\") {
		return fail(termproto.FileErrURLBlocked, "网址缺少主机名")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fail(termproto.FileErrURLBlocked, "网址的端口无效")
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil && !p.AllowPrivate && !publicAddr(ip) {
		return fail(termproto.FileErrURLBlocked, blockedMsg)
	}
	return nil
}

func (p URLPolicy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if p.Lookup != nil {
		return p.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// control 是拨号的 Control 钩子：在 connect 之前校验「实际要连的地址」。
func (p URLPolicy) control(network, address string, _ syscall.RawConn) error {
	if p.AllowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlocked
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip) {
		return errBlocked
	}
	return nil
}

// dialContext 解析主机名，要求全部地址合规，然后直接拨号到已校验的 IP。
func (p URLPolicy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errBlocked
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = p.lookup(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errBlocked // 无法解析与非公网不做区分
		}
	}
	if !p.AllowPrivate {
		for _, ip := range ips {
			if !publicAddr(ip) {
				return nil, errBlocked
			}
		}
	}
	dial := p.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: dialTimeout, Control: p.control}
		dial = d.DialContext
	}
	var last error
	for _, ip := range ips {
		conn, derr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
		if errors.Is(derr, errBlocked) || ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// newClient 构造只用于 push_url 的 HTTP 客户端。timeout 覆盖整个交换（含读取响应体）。
func (p URLPolicy) newClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                  nil, // 绝不使用代理：代理会替我们解析目标主机名，校验就被绕过了
		DialContext:            p.dialContext,
		TLSHandshakeTimeout:    tlsTimeout,
		ResponseHeaderTimeout:  headerTimeout,
		MaxResponseHeaderBytes: maxHeaderBytes,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			if e := p.checkURL(req.URL); e != nil {
				return errBlocked
			}
			return nil
		},
	}
}

// urlSource 给出网址的「协议://主机[:端口]」形式（不含路径、查询串与片段，查询串里常带签名）。
func urlSource(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}
