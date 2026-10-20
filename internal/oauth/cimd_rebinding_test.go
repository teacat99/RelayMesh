package oauth

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

func TestCIMDDialRejectsPrivateAtConnectTime(t *testing.T) {
	ResetCIMDNetHooksForTest()
	defer ResetCIMDNetHooksForTest()
	public := netip.MustParseAddr("93.184.216.34")
	SetCIMDNetHooksForTest(
		func(ctx context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{public}, nil
		},
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 模拟 DNS 重绑定：解析为公网，连接时落到回环。
			return net.Dial(network, net.JoinHostPort("127.0.0.1", "443"))
		},
	)
	_, err := FetchCIMD(context.Background(), "https://example.com/client.json")
	if err == nil {
		t.Fatal("expected rebinding to be blocked")
	}
}
