package handlers

import (
	"bufio"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/warsmite/gamejanitor/constants"
)

type LogHandlers struct {
	logPath string
	log     *slog.Logger
}

func NewLogHandlers(logPath string, log *slog.Logger) *LogHandlers {
	return &LogHandlers{logPath: logPath, log: log}
}

func (h *LogHandlers) Get(w http.ResponseWriter, r *http.Request) {
	tail := constants.PaginationDefaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	lines, err := tailFile(h.logPath, tail)
	if err != nil {
		if os.IsNotExist(err) {
			respondOK(w, map[string]any{"lines": []string{}})
			return
		}
		h.log.Error("reading log file", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondOK(w, map[string]any{"lines": lines})
}

func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var all []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		all = append(all, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}
