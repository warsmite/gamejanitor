package sftp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	gosftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPAuth validates SFTP login credentials and returns the gameserver ID and volume name.
type SFTPAuth interface {
	ValidateLogin(username, password string) (gameserverID string, volumeName string, err error)
}

// FileOperator abstracts volume-level file operations for the SFTP handler.
type FileOperator interface {
	ListFiles(volumeName string, path string) ([]FileEntry, error)
	ReadFile(volumeName string, path string) ([]byte, error)
	WriteFile(volumeName string, path string, content []byte, perm os.FileMode) error
	DeletePath(volumeName string, path string) error
	CreateDirectory(volumeName string, path string) error
	RenamePath(volumeName string, from string, to string) error
}

// FileEntry represents a file or directory in a volume.
type FileEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime int64
}

// FileOperatorFactory creates a FileOperator for a given session.
// On workers, this returns the same WorkerFileOperator each time.
// On controllers, this creates a DispatcherFileOperator scoped to the gameserver.
type FileOperatorFactory func(gameserverID string) FileOperator

type Server struct {
	listener      net.Listener
	sshConfig     *ssh.ServerConfig
	auth          SFTPAuth
	fileOpFactory FileOperatorFactory
	rateLimiter   *authRateLimiter
	log           *slog.Logger
	done          chan struct{}
}

func NewServer(auth SFTPAuth, fileOpFactory FileOperatorFactory, hostKeyPath string, log *slog.Logger) (*Server, error) {
	hostKey, err := loadOrCreateHostKey(hostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("loading sftp host key: %w", err)
	}

	s := &Server{
		auth:          auth,
		fileOpFactory: fileOpFactory,
		rateLimiter:   newAuthRateLimiter(5, 15*time.Minute),
		log:           log,
		done:          make(chan struct{}),
	}

	config := &ssh.ServerConfig{
		PasswordCallback: s.passwordCallback,
	}
	config.AddHostKey(hostKey)
	s.sshConfig = config

	return s, nil
}

func (s *Server) passwordCallback(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

	if err := s.rateLimiter.check(ip); err != nil {
		s.log.Warn("sftp login blocked by rate limiter", "ip", ip, "user", conn.User())
		return nil, fmt.Errorf("too many failed attempts, try again later")
	}

	gameserverID, volumeName, err := s.auth.ValidateLogin(conn.User(), string(password))
	if err != nil {
		s.rateLimiter.recordFailure(ip)
		s.log.Info("sftp login failed", "ip", ip, "user", conn.User())
		return nil, err
	}

	s.rateLimiter.recordSuccess(ip)
	return &ssh.Permissions{
		Extensions: map[string]string{
			"gameserver_id": gameserverID,
			"volume_name":   volumeName,
		},
	}, nil
}

func (s *Server) ListenAndServe(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("sftp listen on %s: %w", addr, err)
	}
	s.listener = listener
	s.log.Info("sftp server listening", "addr", addr)

	go s.cleanupLoop()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
				s.log.Error("accepting sftp connection", "error", err)
				continue
			}
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) Close() error {
	close(s.done)
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.rateLimiter.cleanup()
		}
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		s.log.Debug("sftp handshake failed", "remote", conn.RemoteAddr(), "error", err)
		conn.Close()
		return
	}
	defer sshConn.Close()

	gameserverID := sshConn.Permissions.Extensions["gameserver_id"]
	volumeName := sshConn.Permissions.Extensions["volume_name"]
	s.log.Info("sftp session started", "remote", sshConn.RemoteAddr(), "gameserver", gameserverID)

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			s.log.Error("accepting sftp channel", "error", err)
			continue
		}

		fileOp := s.fileOpFactory(gameserverID)
		go s.handleChannel(channel, requests, gameserverID, volumeName, fileOp)
	}
}

func (s *Server) handleChannel(channel ssh.Channel, requests <-chan *ssh.Request, gameserverID, volumeName string, fileOp FileOperator) {
	defer channel.Close()

	for req := range requests {
		if req.Type != "subsystem" || string(req.Payload[4:]) != "sftp" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		req.Reply(true, nil)

		h := newHandler(fileOp, volumeName, s.log)
		server := gosftp.NewRequestServer(channel, h.Handlers())
		if err := server.Serve(); err != nil && err != io.EOF {
			s.log.Error("sftp session error", "gameserver", gameserverID, "error", err)
		}
		server.Close()
		return
	}
}

func loadOrCreateHostKey(keyPath string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err == nil {
		return ssh.ParsePrivateKey(keyBytes)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating sftp host key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling sftp host key: %w", err)
	}

	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err != nil {
		return nil, fmt.Errorf("creating sftp host key directory: %w", err)
	}
	if err := os.WriteFile(keyPath, pemBlock, 0600); err != nil {
		return nil, fmt.Errorf("writing sftp host key: %w", err)
	}

	return ssh.ParsePrivateKey(pemBlock)
}
