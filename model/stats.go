package model

import "time"

type StatsSample struct {
	GameserverID    string    `json:"gameserver_id"`
	Resolution      string    `json:"resolution,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryUsageMB   int       `json:"memory_usage_mb"`
	MemoryLimitMB   int       `json:"memory_limit_mb"`
	NetRxBytes      int64     `json:"net_rx_bytes"`
	NetTxBytes      int64     `json:"net_tx_bytes"`
	VolumeSizeBytes int64     `json:"volume_size_bytes"`
	PlayersOnline   int       `json:"players_online"`
}

type StatsPeriod string

const (
	StatsPeriod1h  StatsPeriod = "1h"
	StatsPeriod24h StatsPeriod = "24h"
	StatsPeriod7d  StatsPeriod = "7d"
)

func ValidStatsPeriod(s string) (StatsPeriod, bool) {
	switch StatsPeriod(s) {
	case StatsPeriod1h, StatsPeriod24h, StatsPeriod7d:
		return StatsPeriod(s), true
	default:
		return "", false
	}
}
