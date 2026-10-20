package oauth

import (
	"context"
	"fmt"
	"net"
)

// cimdLookupHost 解析 CIMD 文档 URL 的主机；测试可注入以绕过真实 DNS。
var cimdLookupHost = defaultCIMDLookupHost

func defaultCIMDLookupHost(ctx context.Context, host string) error {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("invalid_client")
	}
	for _, a := range addrs {
		if ipPrivate(a.IP) {
			return fmt.Errorf("invalid_client")
		}
	}
	return nil
}

func cimdEnsurePublicHost(ctx context.Context, host string) error {
	return cimdLookupHost(ctx, host)
}

// SetCIMDLookupHost 供测试注入 DNS 解析（返回 nil 表示通过）。
func SetCIMDLookupHost(fn func(ctx context.Context, host string) error) {
	if fn == nil {
		cimdLookupHost = defaultCIMDLookupHost
		return
	}
	cimdLookupHost = fn
}
