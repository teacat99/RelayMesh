package oauth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cimdHTTPSPort = "443"

var errCIMDBlocked = errors.New("cimd blocked")

// cimdNetHooks 仅供同包测试注入网络行为（生产路径为零值）。
type cimdNetHooks struct {
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial   func(ctx context.Context, network, addr string) (net.Conn, error)
}

var cimdHooks cimdNetHooks

func cimdPublicAddr(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || !a.IsGlobalUnicast() {
		return false
	}
	blocked := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
		"::ffff:0:0/96", "fc00::/7", "fe80::/10", "ff00::/8",
	}
	for _, p := range blocked {
		prefix, err := netip.ParsePrefix(p)
		if err == nil && prefix.Contains(a) {
			return false
		}
	}
	return true
}

func cimdCheckURL(u *url.URL) error {
	if u.Scheme != "https" {
		return errCIMDBlocked
	}
	if u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return errCIMDBlocked
	}
	host := u.Hostname()
	if host == "" {
		return errCIMDBlocked
	}
	port := u.Port()
	if port == "" {
		port = cimdHTTPSPort
	}
	if port != cimdHTTPSPort {
		return errCIMDBlocked
	}
	if ip, err := netip.ParseAddr(host); err == nil && !cimdPublicAddr(ip) {
		return errCIMDBlocked
	}
	return nil
}

func (h cimdNetHooks) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if h.Lookup != nil {
		return h.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (h cimdNetHooks) control(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errCIMDBlocked
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !cimdPublicAddr(ip) {
		return errCIMDBlocked
	}
	return nil
}

func (h cimdNetHooks) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if h.Dial != nil {
		return h.Dial(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errCIMDBlocked
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = h.lookup(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errCIMDBlocked
		}
	}
	for _, ip := range ips {
		if !cimdPublicAddr(ip) {
			return nil, errCIMDBlocked
		}
	}
	d := &net.Dialer{Timeout: cimdTimeout, Control: h.control}
	var last error
	for _, ip := range ips {
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
		if errors.Is(derr, errCIMDBlocked) || ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

func newSecureCIMDClient() *http.Client {
	h := cimdHooks
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return h.dialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cimdTimeout,
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   cimdTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func parseCIMDFetchURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "https://") {
		return nil, fmt.Errorf("invalid_client")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid_client")
	}
	if err := cimdCheckURL(u); err != nil {
		return nil, fmt.Errorf("invalid_client")
	}
	return u, nil
}

// SetCIMDNetHooksForTest 注入 CIMD 网络钩子（仅测试）。
func SetCIMDNetHooksForTest(lookup func(context.Context, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) {
	cimdHooks.Lookup = lookup
	cimdHooks.Dial = dial
}

// ResetCIMDNetHooksForTest 恢复默认 CIMD 网络行为。
func ResetCIMDNetHooksForTest() {
	cimdHooks = cimdNetHooks{}
}

// cimdHostForDial 规范化主机用于拨号（支持十进制整数 IP 等，在 check 阶段已拒绝私网字面量）。
func cimdPort(u *url.URL) string {
	p := u.Port()
	if p == "" {
		return cimdHTTPSPort
	}
	if n, err := strconv.Atoi(p); err != nil || n != 443 {
		return ""
	}
	return p
}
