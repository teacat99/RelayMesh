package cert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GetOrCreateTLSCertificate 加载已有证书或自动生成并持久化自签名证书
func GetOrCreateTLSCertificate(certPath, keyPath string) (tls.Certificate, error) {
	if certPath != "" && keyPath != "" {
		if fileExists(certPath) && fileExists(keyPath) {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err == nil {
				return cert, nil
			}
			log.Printf("Warning: failed to load existing TLS certificate from %s/%s: %v, regenerating...", certPath, keyPath, err)
		}
	}

	// 生成新的自签名证书
	certPEM, keyPEM, err := GenerateSelfSignedCert()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	// 尝试持久化到磁盘
	if certPath != "" && keyPath != "" {
		if err := os.MkdirAll(filepath.Dir(certPath), 0755); err == nil {
			_ = os.WriteFile(certPath, certPEM, 0644)
		}
		if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err == nil {
			_ = os.WriteFile(keyPath, keyPEM, 0600)
		}
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// GenerateSelfSignedCert 自动为 localhost 及当前主机所有网卡 IP 生成自签名证书
func GenerateSelfSignedCert() ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(10 * 365 * 24 * time.Hour) // 10 年有效期

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"RelayMesh"},
			CommonName:   "RelayMesh Self-Signed Certificate",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "relaymesh.local"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("0.0.0.0")},
	}

	// 自动收集当前主机的所有网络接口 IP 并加入 SAN
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ipNet.IP.To4() != nil {
					template.IPAddresses = append(template.IPAddresses, ipNet.IP)
				}
			}
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return certPEM, keyPEM, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
