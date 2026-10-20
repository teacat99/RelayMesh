package sshexec_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
)

// 经网关接入的主机（Spec.Via 非空）本进程永远不能直连：Dial 与连接池都在碰网络之前就拒绝。
func TestViaGatewaySpecIsNeverDialed(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{User: "tester", Password: "pw-via-test-1"})
	sp := sshexec.Spec{
		Addr: srv.Addr, User: "tester", Via: "RM-ABCD-EFGH-JKMN-PQRS",
		Cred:    sshexec.Credential{Kind: sshexec.AuthPassword, Password: "pw-via-test-1"},
		HostKey: sshexec.NewHostKey(srv.HostKey()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sshexec.Dial(ctx, sp, time.Second); !errors.Is(err, sshexec.ErrViaGateway) {
		t.Fatalf("Dial 应拒绝 Via 规格: %v", err)
	}
	pool := &sshexec.Pool{}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "k", sp, "true", nil, 0); !errors.Is(err, sshexec.ErrViaGateway) {
		t.Fatalf("连接池应拒绝 Via 规格: %v", err)
	}
	if n := srv.Connections(); n != 0 {
		t.Fatalf("被拒绝的规格不应产生任何网络连接，实际 %d 条", n)
	}

	// 去掉 Via 之后同一份规格可以正常连接（说明被拒绝的原因只有 Via）。
	sp.Via = ""
	c, err := sshexec.Dial(ctx, sp, 5*time.Second)
	if err != nil {
		t.Fatalf("没有 Via 时应能连接: %v", err)
	}
	_ = c.Close()
}
