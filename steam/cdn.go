package steam

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
}

// NewCDNClient creates a CDN client with the given server hosts.
func NewCDNClient(log *slog.Logger, hosts []string) *CDNClient {
	return &CDNClient{
		log:   log.With("component", "steam_cdn"),
		hosts: hosts,
		httpClient: &http.Client{
			Timeout: cdnRequestTimeout,
		},
	}
}

// DownloadChunk downloads a single chunk from the CDN and returns the raw (encrypted+compressed) data.
// Tries each host in the list, skipping hosts that fail with TLS or connection errors.
func (c *CDNClient) DownloadChunk(ctx context.Context, depotID uint32, chunkIDHex string) ([]byte, error) {
	var lastErr error

	// Try up to len(hosts) different servers to find one that works
	maxAttempts := max(defaultChunkRetries, len(c.hosts))
	for attempt := range maxAttempts {
		host := c.nextHost()
		url := fmt.Sprintf("https://%s/depot/%d/chunk/%s", host, depotID, chunkIDHex)

		data, err := c.doDownload(ctx, url)
		if err != nil {
			lastErr = err
			c.log.Debug("CDN chunk download failed, trying next host",
				"chunk_id", chunkIDHex,
				"host", host,
				"attempt", attempt+1,
				"error", err,
			)
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
					magic := ""
					if len(raw) > 20 {
						if dec, err2 := steamDecrypt(raw, depotKey); err2 == nil && len(dec) >= 4 {
							magic = fmt.Sprintf("%02x%02x%02x%02x", dec[0], dec[1], dec[2], dec[3])
						}
					}
					errCh <- fmt.Errorf("chunk %s: size mismatch (got %d, want %d, compressed=%d, magic=%s)", w.chunk.ChunkIDHex(), len(data), w.chunk.DecompressedSize, w.chunk.CompressedSize, magic)
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

	// Return the first error
	for err := range errCh {
		return err
	}

	return nil
}

func (c *CDNClient) nextHost() string {
	c.hostMu.Lock()
	defer c.hostMu.Unlock()

	host := c.hosts[c.hostIdx%len(c.hosts)]
	c.hostIdx++
	return host
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
