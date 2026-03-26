package service

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// UsedHostPorts returns all host ports in use.
// In cluster scope: checks all nodes (ports are cluster-unique).
// In node scope: checks only the given node.
func (s *GameserverService) UsedHostPorts(nodeID string, excludeID string) (map[int]bool, error) {
	var filter model.GameserverFilter
	if s.settingsSvc.GetString(SettingPortUniqueness) == "node" {
		filter.NodeID = &nodeID
	}

	allGS, err := model.ListGameservers(s.db, filter)
	if err != nil {
		return nil, fmt.Errorf("listing gameservers for port check: %w", err)
	}

	used := make(map[int]bool)
	for _, gs := range allGS {
		if gs.ID == excludeID {
			continue
		}
		var ports []portMapping
		if err := json.Unmarshal(gs.Ports, &ports); err != nil {
			continue
		}
		for _, p := range ports {
			if hp := int(p.HostPort); hp != 0 {
				used[hp] = true
			}
		}
	}
	return used, nil
}

func (s *GameserverService) portRange() (int, int) {
	return s.settingsSvc.GetInt(SettingPortRangeStart), s.settingsSvc.GetInt(SettingPortRangeEnd)
}

// checkWorkerLimits returns an error if the worker has exceeded its configured resource limits.
func (s *GameserverService) checkWorkerLimits(nodeID string, memoryNeeded int, cpuNeeded float64, storageNeeded int) error {
	node, err := model.GetWorkerNode(s.db, nodeID)
	if err != nil || node == nil {
		return nil // no node record = no limits
	}

	if node.MaxMemoryMB != nil {
		allocated, err := model.AllocatedMemoryByNode(s.db, nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+memoryNeeded > *node.MaxMemoryMB {
			return ErrUnavailablef("worker %s has reached its memory limit (%d MB allocated, %d MB limit)", nodeID, allocated, *node.MaxMemoryMB)
		}
	}

	if node.MaxCPU != nil {
		allocated, err := model.AllocatedCPUByNode(s.db, nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+cpuNeeded > *node.MaxCPU {
			return ErrUnavailablef("worker %s has reached its CPU limit (%.1f allocated, %.1f limit)", nodeID, allocated, *node.MaxCPU)
		}
	}

	if node.MaxStorageMB != nil && storageNeeded > 0 {
		allocated, err := model.AllocatedStorageByNode(s.db, nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+storageNeeded > *node.MaxStorageMB {
			return ErrUnavailablef("worker %s has reached its storage limit (%d MB allocated, %d MB limit)", nodeID, allocated, *node.MaxStorageMB)
		}
	}

	return nil
}

// checkWorkerLimitsExcluding is like checkWorkerLimits but excludes one gameserver's allocation.
// Used by auto-migration to check if a node can still fit after a resource update.
func (s *GameserverService) checkWorkerLimitsExcluding(nodeID string, memoryNeeded int, cpuNeeded float64, storageNeeded int, excludeID string) error {
	node, err := model.GetWorkerNode(s.db, nodeID)
	if err != nil || node == nil {
		return nil
	}

	if node.MaxMemoryMB != nil {
		allocated, err := model.AllocatedMemoryByNodeExcluding(s.db, nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+memoryNeeded > *node.MaxMemoryMB {
			return ErrUnavailablef("worker %s would exceed memory limit (%d MB allocated + %d MB needed > %d MB limit)", nodeID, allocated, memoryNeeded, *node.MaxMemoryMB)
		}
	}

	if node.MaxCPU != nil {
		allocated, err := model.AllocatedCPUByNodeExcluding(s.db, nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+cpuNeeded > *node.MaxCPU {
			return ErrUnavailablef("worker %s would exceed CPU limit (%.1f allocated + %.1f needed > %.1f limit)", nodeID, allocated, cpuNeeded, *node.MaxCPU)
		}
	}

	if node.MaxStorageMB != nil && storageNeeded > 0 {
		allocated, err := model.AllocatedStorageByNodeExcluding(s.db, nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+storageNeeded > *node.MaxStorageMB {
			return ErrUnavailablef("worker %s would exceed storage limit (%d MB allocated + %d MB needed > %d MB limit)", nodeID, allocated, storageNeeded, *node.MaxStorageMB)
		}
	}

	return nil
}

func ptrIntOr0(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

// AllocatePorts finds a contiguous block of free host ports for the game's port requirements.
func (s *GameserverService) AllocatePorts(game *games.Game, nodeID string, excludeID string) (json.RawMessage, error) {
	gamePorts := game.DefaultPorts
	if len(gamePorts) == 0 {
		return json.RawMessage("[]"), nil
	}

	// Find unique port numbers in order
	seen := make(map[int]bool)
	var uniquePorts []int
	for _, p := range gamePorts {
		if !seen[p.Port] {
			seen[p.Port] = true
			uniquePorts = append(uniquePorts, p.Port)
		}
	}
	sort.Ints(uniquePorts)
	blockSize := len(uniquePorts)

	// Build mapping from original port number to its index (for assignment)
	portIndex := make(map[int]int)
	for i, p := range uniquePorts {
		portIndex[p] = i
	}

	rangeStart, rangeEnd := s.portRange()

	used, err := s.UsedHostPorts(nodeID, excludeID)
	if err != nil {
		return nil, err
	}

	// Find first contiguous block of blockSize free ports
	base := -1
	for candidate := rangeStart; candidate+blockSize-1 <= rangeEnd; candidate++ {
		free := true
		for offset := 0; offset < blockSize; offset++ {
			if used[candidate+offset] {
				free = false
				candidate = candidate + offset // skip ahead
				break
			}
		}
		if free {
			base = candidate
			break
		}
	}

	if base == -1 {
		return nil, fmt.Errorf("no contiguous block of %d ports available in range %d-%d", blockSize, rangeStart, rangeEnd)
	}

	// Map game ports to allocated ports
	result := make([]portMapping, len(gamePorts))
	for i, p := range gamePorts {
		allocatedPort := base + portIndex[p.Port]
		result[i] = portMapping{
			Name:          p.Name,
			HostPort:      flexInt(allocatedPort),
			ContainerPort: flexInt(allocatedPort),
			Protocol:      p.Protocol,
		}
	}

	s.log.Info("auto-allocated ports", "game", game.ID, "base", base, "block_size", blockSize)

	return json.Marshal(result)
}
