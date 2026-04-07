package handler

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
)

type LogHandlers struct {
	logPath string
	log     *slog.Logger
}

func NewLogHandlers(logPath string, log *slog.Logger) *LogHandlers {
	return &LogHandlers{logPath: logPath, log: log}
}

func (h *LogHandlers) Get(w http.ResponseWriter, r *http.Request) {
	tail := PaginationDefaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	lines, err := tailFile(h.logPath, tail)
	if err != nil {
		if os.IsNotExist(err) {
			respondOK(w, struct {
				Lines []string `json:"lines"`
			}{Lines: []string{}})
			return
		}
		h.log.Error("reading log file", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to read log file")
		return
	}

	respondOK(w, struct {
		Lines []string `json:"lines"`
	}{Lines: lines})
}

// tailFile reads the last n lines from a file by seeking backward from the end.
// Only reads enough bytes to find the requested lines, avoiding loading the
// entire file into memory.
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size == 0 {
		return []string{}, nil
	}

	// Read backward in 8KB blocks, counting newlines until we have enough
	const blockSize = 8192
	linesFound := 0
	offset := size

	for offset > 0 && linesFound <= n {
		readSize := int64(blockSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
			return nil, err
		}

		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				linesFound++
				if linesFound > n {
					// Found enough — read from this position forward
					offset += int64(i) + 1
					break
				}
			}
		}
	}

	// Read from the computed offset to end of file
	remaining := size - offset
	buf := make([]byte, remaining)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return nil, err
	}

	// Split into lines, trimming trailing empty line from final newline
	var lines []string
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			lines = append(lines, string(buf[start:i]))
			start = i + 1
		}
	}
	if start < len(buf) {
		lines = append(lines, string(buf[start:]))
	}

	return lines, nil
}
