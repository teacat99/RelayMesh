package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cimdMaxBytes = 64 << 10
	cimdTimeout  = 3 * time.Second
)

// HTTPDoer 供测试注入；生产使用 net/http.Client。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	defaultHTTPClient          = newSecureCIMDClient()
	cimdHTTPDoer      HTTPDoer = defaultHTTPClient
	cimdCacheMu       sync.RWMutex
	cimdCache         = map[string]cimdCached{}
	cimdNow           = time.Now
	cimdCacheTTL      = 15 * time.Minute
)

// ClearCIMDCache 供测试清空 CIMD 缓存。
func ClearCIMDCache() {
	cimdCacheMu.Lock()
	cimdCache = map[string]cimdCached{}
	cimdCacheMu.Unlock()
}

// SetCIMDCacheClock 供测试注入缓存时钟。
func SetCIMDCacheClock(fn func() time.Time) {
	if fn == nil {
		cimdNow = time.Now
		return
	}
	cimdNow = fn
}

type cimdCached struct {
	doc       CIMDDocument
	fetchedAt time.Time
}

// CIMDDocument 是客户端元数据文档的最小字段集。
type CIMDDocument struct {
	ClientID      string   `json:"client_id"`
	ClientName    string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
	TokenAuth     string   `json:"token_endpoint_auth_method"`
}

func SetCIMDHTTPDoer(d HTTPDoer) {
	if d == nil {
		cimdHTTPDoer = defaultHTTPClient
		return
	}
	cimdHTTPDoer = d
}

func FetchCIMD(ctx context.Context, clientIDURL string) (CIMDDocument, error) {
	clientIDURL = strings.TrimSpace(clientIDURL)
	if !strings.HasPrefix(strings.ToLower(clientIDURL), "https://") {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	cimdCacheMu.RLock()
	if c, ok := cimdCache[clientIDURL]; ok && cimdNow().Sub(c.fetchedAt) < cimdCacheTTL {
		cimdCacheMu.RUnlock()
		return c.doc, nil
	}
	cimdCacheMu.RUnlock()

	if _, err := parseCIMDFetchURL(clientIDURL); err != nil {
		return CIMDDocument{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientIDURL, nil)
	if err != nil {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	resp, err := cimdHTTPDoer.Do(req)
	if err != nil {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cimdMaxBytes+1))
	if err != nil {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	if len(body) > cimdMaxBytes {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	doc, err := parseCIMDBody(body, clientIDURL)
	if err != nil {
		return CIMDDocument{}, err
	}
	cimdCacheMu.Lock()
	cimdCache[clientIDURL] = cimdCached{doc: doc, fetchedAt: cimdNow()}
	cimdCacheMu.Unlock()
	return doc, nil
}

func parseCIMDBody(body []byte, clientIDURL string) (CIMDDocument, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	for _, forbidden := range []string{"client_secret", "client_secret_expires_at"} {
		if _, ok := raw[forbidden]; ok {
			return CIMDDocument{}, fmt.Errorf("invalid_client")
		}
	}
	var doc CIMDDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	if strings.TrimSpace(doc.ClientID) != clientIDURL {
		return CIMDDocument{}, fmt.Errorf("invalid_client")
	}
	if err := validateCIMDDocument(doc); err != nil {
		return CIMDDocument{}, err
	}
	return doc, nil
}

func validateCIMDDocument(doc CIMDDocument) error {
	auth := strings.TrimSpace(doc.TokenAuth)
	if auth != "" && auth != "none" {
		return fmt.Errorf("invalid_client")
	}
	return ValidateRedirectURIList(doc.RedirectURIs)
}

func rejectPrivateTarget(host string) error {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return fmt.Errorf("invalid_client")
	}
	if ip := net.ParseIP(host); ip != nil && ipPrivate(ip) {
		return fmt.Errorf("invalid_client")
	}
	return nil
}

func ipPrivate(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
