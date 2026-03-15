package worker

import (
	"bufio"
	"encoding/binary"
	"io"
	"strings"
)

// ParseLogLines reads Docker's multiplexed log stream and returns all lines.
// Stderr lines are prefixed with "[ERR] ".
func ParseLogLines(r io.Reader) []string {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)
	var lines []string

	for {
		if _, err := io.ReadFull(br, header); err != nil {
			break
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			break
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines = append(lines, prefix+line)
			}
		}
	}

	return lines
}

// ParseLogStream reads Docker's multiplexed log stream and sends lines to the channel.
// Stderr lines are prefixed with "[ERR] ". Closes when the stream ends.
func ParseLogStream(r io.Reader, lines chan<- string) {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)

	for {
		if _, err := io.ReadFull(br, header); err != nil {
			return
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines <- prefix + line
			}
		}
	}
}
