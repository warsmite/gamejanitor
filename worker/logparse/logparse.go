// Package logparse handles parsing of instance log streams.
// Retains Docker multiplexed log format detection for compatibility —
// log streams from external sources or older setups may still use this format.
// The auto-detection is harmless: non-Docker streams fall through to raw text parsing.
package logparse

import (
	"bufio"
	"encoding/binary"
	"io"
	"strings"
)

// isDockerMultiplexed checks if the stream starts with a Docker log frame header.
// Docker multiplexed streams start with 0x01 (stdout) or 0x02 (stderr).
func isDockerMultiplexed(br *bufio.Reader) bool {
	peek, err := br.Peek(1)
	if err != nil || len(peek) == 0 {
		return false
	}
	return peek[0] == 1 || peek[0] == 2
}

// ParseLogLines reads a log stream and returns all lines.
// Auto-detects Docker multiplexed format vs raw text.
func ParseLogLines(r io.Reader) []string {
	br := bufio.NewReaderSize(r, 32*1024)

	if !isDockerMultiplexed(br) {
		return parseRawLogLines(br)
	}
	return parseDockerLogLines(br)
}

// ParseLogStream reads a log stream and sends lines to the channel.
// Auto-detects Docker multiplexed format vs raw text. Closes when the stream ends.
func ParseLogStream(r io.Reader, lines chan<- string) {
	br := bufio.NewReaderSize(r, 32*1024)

	if !isDockerMultiplexed(br) {
		parseRawLogStream(br, lines)
		return
	}
	parseDockerLogStream(br, lines)
}

func parseRawLogLines(br *bufio.Reader) []string {
	var lines []string
	scanner := bufio.NewScanner(br)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseRawLogStream(br *bufio.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(br)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines <- line
		}
	}
}

func parseDockerLogLines(br *bufio.Reader) []string {
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

func parseDockerLogStream(br *bufio.Reader, lines chan<- string) {
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
