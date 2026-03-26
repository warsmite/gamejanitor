package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func tlsDir(dataDir string) string {
	return filepath.Join(dataDir, "tls")
}

// LoadOrCreateCA loads an existing CA or generates a new one.
// Files: {dataDir}/tls/ca.crt, {dataDir}/tls/ca.key
func LoadOrCreateCA(dataDir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	dir := tlsDir(dataDir)
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	// Try loading existing
	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("reading CA key: %w", err)
		}
		return parseCertAndKey(certPEM, keyPEM)
	}

	// Generate new CA
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Gamejanitor"}, CommonName: "Gamejanitor CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	if err := writeCertAndKey(dir, "ca", certDER, key); err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// LoadOrCreateServerCert loads or generates a server certificate signed by the CA.
// Files: {dataDir}/tls/server.crt, {dataDir}/tls/server.key
func LoadOrCreateServerCert(dataDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (tls.Certificate, error) {
	dir := tlsDir(dataDir)
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")

	if _, err := os.Stat(certPath); err == nil {
		return tls.LoadX509KeyPair(certPath, keyPath)
	}

	return generateSignedCert(dir, "server", caCert, caKey, true)
}

// GenerateWorkerCert generates a worker certificate signed by the CA.
// Files: {dataDir}/tls/worker-{id}.crt, {dataDir}/tls/worker-{id}.key
// Returns paths to the generated files.
func GenerateWorkerCert(dataDir string, workerID string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (certPath, keyPath, caPath string, err error) {
	dir := tlsDir(dataDir)
	name := "worker-" + workerID

	if _, err := generateSignedCert(dir, name, caCert, caKey, false); err != nil {
		return "", "", "", err
	}

	return filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key"), filepath.Join(dir, "ca.crt"), nil
}

// GenerateWorkerCertPEM generates a worker certificate signed by the CA and returns PEM bytes.
// The cert includes SANs for the provided worker IPs plus localhost.
func GenerateWorkerCertPEM(workerID string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, workerIPs []net.IP) (caPEM, certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating worker key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Gamejanitor"}, CommonName: "worker-" + workerID},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  append([]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, workerIPs...),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating worker certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshaling worker key: %w", err)
	}

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	certOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return caCertPEM, certOut, keyOut, nil
}

// ServerTLSConfig returns a TLS config for the gRPC server with mTLS.
func ServerTLSConfig(dataDir string) (*tls.Config, error) {
	dir := tlsDir(dataDir)
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	caPath := filepath.Join(dir, "ca.crt")

	// If no server cert exists, no TLS
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, nil
	}

	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading server cert: %w", err)
	}

	caPool, err := LoadCACertPool(caPath)
	if err != nil {
		return nil, fmt.Errorf("loading CA for server: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}

// ClientTLSConfig returns a TLS config for gRPC clients with client cert.
func ClientTLSConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	caPool, err := LoadCACertPool(caPath)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading client cert: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
	}, nil
}

// LoadCACertPool loads a CA certificate file into a cert pool.
func LoadCACertPool(caPath string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA cert from %s", caPath)
	}
	return pool, nil
}

func generateSignedCert(dir, name string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, isServer bool) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating %s key: %w", name, err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Gamejanitor"}, CommonName: name},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(5 * 365 * 24 * time.Hour),
	}

	if isServer {
		template.KeyUsage = x509.KeyUsageDigitalSignature
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
		// SANs for local development + all detected IPs
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
					template.IPAddresses = append(template.IPAddresses, ipNet.IP)
				}
			}
		}
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
		// Worker certs also need ServerAuth for the dial-back connection
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
					template.IPAddresses = append(template.IPAddresses, ipNet.IP)
				}
			}
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating %s certificate: %w", name, err)
	}

	if err := writeCertAndKey(dir, name, certDER, key); err != nil {
		return tls.Certificate{}, err
	}

	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func writeCertAndKey(dir, name string, certDER []byte, key *ecdsa.PrivateKey) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating tls directory: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0644); err != nil {
		return fmt.Errorf("writing %s cert: %w", name, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshaling %s key: %w", name, err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0600); err != nil {
		return fmt.Errorf("writing %s key: %w", name, err)
	}

	return nil
}

func parseCertAndKey(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA key: %w", err)
	}

	return cert, key, nil
}
