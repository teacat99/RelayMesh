package oauth

import (
	"strings"
	"testing"
)

func TestNormalizeClientHostAllowlist(t *testing.T) {
	out, err := NormalizeClientHostAllowlist([]string{" Example.COM ", "example.com", "tools.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "example.com" || out[1] != "tools.example.com" {
		t.Fatalf("got %v", out)
	}
	_, err = NormalizeClientHostAllowlist([]string{"https://evil.com"})
	if err == nil || !strings.Contains(err.Error(), "协议") {
		t.Fatalf("want protocol error got %v", err)
	}
	_, err = NormalizeClientHostAllowlist([]string{"*.example.com"})
	if err == nil {
		t.Fatal("want wildcard rejected")
	}
}
