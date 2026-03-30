package steam

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultCDNWorkers   = 8
	defaultChunkRetries = 3
	cdnRequestTimeout   = 30 * time.Second
)

// CDNClient downloads chunks from Steam's content delivery network.
type CDNClient struct {
	log        *slog.Logger
	httpClient *http.Client
	hosts      []string
	hostIdx    int
	hostMu     sync.Mutex

	// Dynamic blacklist — hosts that fail with TLS/connection errors are
	// skipped for the remainder of this session.
	blacklist   map[string]struct{}
	blacklistMu sync.RWMutex
}

// NewCDNClient creates a CDN client with the given server hosts.
func NewCDNClient(log *slog.Logger, hosts []string) *CDNClient {
	return &CDNClient{
		log:       log.With("component", "steam_cdn"),
		hosts:     hosts,
		blacklist: make(map[string]struct{}),
		httpClient: &http.Client{
			Timeout: cdnRequestTimeout,
		},
	}
}

// DownloadChunk downloads a single chunk from the CDN and returns the raw (encrypted+compressed) data.
func (c *CDNClient) DownloadChunk(ctx context.Context, depotID uint32, chunkIDHex string) ([]byte, error) {
	var lastErr error

	maxAttempts := max(defaultChunkRetries, len(c.hosts))
	for attempt := range maxAttempts {
		host := c.nextAvailableHost()
		if host == "" {
			break
		}
		url := fmt.Sprintf("https://%s/depot/%d/chunk/%s", host, depotID, chunkIDHex)

		data, err := c.doDownload(ctx, url)
		if err != nil {
			lastErr = err

			// Blacklist hosts with TLS or connection-level failures
			if isCDNHostError(err) {
				c.blacklistHost(host)
				c.log.Warn("CDN host blacklisted for session",
					"host", host,
					"error", err,
				)
			} else {
				c.log.Debug("CDN chunk download failed, trying next host",
					"chunk_id", chunkIDHex,
					"host", host,
					"attempt", attempt+1,
					"error", err,
				)
			}
			continue
		}

		return data, nil
	}

	return nil, fmt.Errorf("chunk %s: all %d download attempts failed: %w", chunkIDHex, maxAttempts, lastErr)
}

// DownloadChunksParallel downloads multiple chunks concurrently, decrypts and decompresses them,
// and calls the handler for each completed chunk. Returns on first error.
func (c *CDNClient) DownloadChunksParallel(ctx context.Context, depotID uint32, depotKey []byte, chunks []ManifestChunk, workers int, handler func(chunk ManifestChunk, data []byte) error) error {
	if workers <= 0 {
		workers = defaultCDNWorkers
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type work struct {
		chunk ManifestChunk
	}

	workCh := make(chan work, len(chunks))
	for _, chunk := range chunks {
		workCh <- work{chunk: chunk}
	}
	close(workCh)

	errCh := make(chan error, workers)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range workCh {
				if ctx.Err() != nil {
					return
				}

				raw, err := c.DownloadChunk(ctx, depotID, w.chunk.ChunkIDHex())
				if err != nil {
					errCh <- err
					cancel()
					return
				}

				data, err := DecryptDepotChunk(raw, depotKey)
				if err != nil {
					errCh <- fmt.Errorf("chunk %s: %w", w.chunk.ChunkIDHex(), err)
					cancel()
					return
				}

				if uint32(len(data)) != w.chunk.DecompressedSize {
					errCh <- fmt.Errorf("chunk %s: size mismatch (got %d, want %d)", w.chunk.ChunkIDHex(), len(data), w.chunk.DecompressedSize)
					cancel()
					return
				}

				if err := handler(w.chunk, data); err != nil {
					errCh <- err
					cancel()
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		return err
	}

	return nil
}

// nextAvailableHost returns the next host that isn't blacklisted.
// Returns empty string if all hosts are blacklisted.
func (c *CDNClient) nextAvailableHost() string {
	c.hostMu.Lock()
	defer c.hostMu.Unlock()

	for range len(c.hosts) {
		host := c.hosts[c.hostIdx%len(c.hosts)]
		c.hostIdx++
		if !c.isBlacklisted(host) {
			return host
		}
	}
	return ""
}

func (c *CDNClient) blacklistHost(host string) {
	c.blacklistMu.Lock()
	c.blacklist[host] = struct{}{}
	c.blacklistMu.Unlock()
}

func (c *CDNClient) isBlacklisted(host string) bool {
	c.blacklistMu.RLock()
	_, ok := c.blacklist[host]
	c.blacklistMu.RUnlock()
	return ok
}

// isCDNHostError returns true if the error indicates a host-level problem
// (TLS, connection refused, DNS) rather than a transient issue.
func isCDNHostError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "certificate") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host")
}

func (c *CDNClient) doDownload(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}
