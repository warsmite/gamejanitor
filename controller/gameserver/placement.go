package gameserver

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// PlacementService handles node selection, capacity checking, and port allocation
// for gameserver placement. All operations that assign a gameserver to a node or
// allocate ports go through this service to serialize concurrent access.
type PlacementService struct {
	store       Store
	dispatcher  *orchestrator.Dispatcher
	settingsSvc *settings.SettingsService
	log         *slog.Logger
	mu          sync.Mutex     // serializes port allocation + node assignment
	portProbe   func(int) bool // nil uses default net.Listen probe
}

func NewPlacementService(store Store, dispatcher *orchestrator.Dispatcher, settingsSvc *settings.SettingsService, log *slog.Logger) *PlacementService {
	return &PlacementService{
		store:       store,
		dispatcher:  dispatcher,
		settingsSvc: settingsSvc,
		log:         log,
	}
}

// SetPortProbe overrides the host port availability check. Used in tests
// where net.Listen probes would interfere with port allocation.
func (p *PlacementService) SetPortProbe(fn func(int) bool) {
	p.portProbe = fn
}

// PlaceGameserver selects a node and allocates ports for a new or relocated gameserver.
// Acquires the placement lock to prevent concurrent port allocation races.
func (p *PlacementService) PlaceGameserver(game *games.Game, gs *model.Gameserver) (nodeID string, ports model.Ports, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if gs.NodeID != nil && *gs.NodeID != "" {
		nodeID = *gs.NodeID
		if err := p.CheckWorkerLimits(nodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
			return "", nil, err
		}
		if gs.PortMode == "auto" {
			ports, err = p.AllocatePorts(game, nodeID, "")
			if err != nil {
				return "", nil, controller.ErrUnavailablef("no available ports for this gameserver")
			}
		}
		return nodeID, ports, nil
	}

	// Auto-place: rank workers, find first with capacity + free ports
	candidates := p.dispatcher.RankWorkersForPlacement(gs.NodeTags)
	if len(candidates) == 0 {
		if !gs.NodeTags.IsEmpty() {
			return "", nil, controller.ErrUnavailablef("no workers available with required labels %v", gs.NodeTags)
		}
		return "", nil, controller.ErrUnavailable("no workers available — connect a worker node first")
	}

	var lastErr error
	for _, c := range candidates {
		if c.NodeID != "" {
			if err := p.CheckWorkerLimits(c.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
				p.log.Debug("worker skipped during placement", "worker", c.NodeID, "reason", err)
				lastErr = err
				continue
			}
		}
		if gs.PortMode == "auto" {
			allocatedPorts, err := p.AllocatePorts(game, c.NodeID, "")
			if err != nil {
				p.log.Debug("worker skipped during placement", "worker", c.NodeID, "reason", err)
				lastErr = err
				continue
			}
			ports = allocatedPorts
		}
		return c.NodeID, ports, nil
	}

	return "", nil, controller.ErrUnavailablef("no worker has capacity for this gameserver: %v", lastErr)
}

// ReallocatePorts allocates new ports for a gameserver on a specific node.
// Used during migration and unarchive when moving to a different node.
func (p *PlacementService) ReallocatePorts(game *games.Game, nodeID string, excludeID string) (model.Ports, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.AllocatePorts(game, nodeID, excludeID)
}

// FindNodeWithCapacity finds a node that can fit the given resources.
// Used by auto-migration when the current node can't fit after a resource update.
func (p *PlacementService) FindNodeWithCapacity(mem int, cpu float64, storage int, tags model.Labels, excludeNodeID string) (string, error) {
	candidates := p.dispatcher.RankWorkersForPlacement(tags)
	for _, c := range candidates {
		if c.NodeID == excludeNodeID {
			continue
		}
		if err := p.CheckWorkerLimits(c.NodeID, mem, cpu, storage); err == nil {
			return c.NodeID, nil
		}
	}
	return "", fmt.Errorf("no node with sufficient capacity for %d MB / %.1f CPU", mem, cpu)
}

// UsedHostPorts returns all host ports in use.
// In cluster scope: checks all nodes (ports are cluster-unique).
// In node scope: checks only the given node.
func (p *PlacementService) UsedHostPorts(nodeID string, excludeID string) (map[int]bool, error) {
	var filter model.GameserverFilter
	if p.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		filter.NodeID = &nodeID
	}

	allGS, err := p.store.ListGameservers(filter)
	if err != nil {
		return nil, fmt.Errorf("listing gameservers for port check: %w", err)
	}

	used := make(map[int]bool)
	for _, gs := range allGS {
		if gs.ID == excludeID {
			continue
		}
		for _, port := range gs.Ports {
			if hp := int(port.HostPort); hp != 0 {
				used[hp] = true
			}
		}
	}
	return used, nil
}

func (p *PlacementService) portRange() (int, int) {
	return p.settingsSvc.GetInt(settings.SettingPortRangeStart), p.settingsSvc.GetInt(settings.SettingPortRangeEnd)
}

// portRangeForNode returns the port range for a specific worker node.
// Uses the worker's per-node range if set, otherwise falls back to the global range.
func (p *PlacementService) portRangeForNode(nodeID string) (int, int) {
	if nodeID != "" {
		node, err := p.store.GetWorkerNode(nodeID)
		if err == nil && node != nil && node.PortRangeStart != nil && node.PortRangeEnd != nil {
			return *node.PortRangeStart, *node.PortRangeEnd
		}
	}
	return p.portRange()
}

// CheckWorkerLimits returns an error if the worker has exceeded its configured resource limits.
func (p *PlacementService) CheckWorkerLimits(nodeID string, memoryNeeded int, cpuNeeded float64, storageNeeded int) error {
	node, err := p.store.GetWorkerNode(nodeID)
	if err != nil || node == nil {
		return nil // no node record = no limits
	}

	if node.MaxMemoryMB != nil {
		allocated, err := p.store.AllocatedMemoryByNode(nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+memoryNeeded > *node.MaxMemoryMB {
			return controller.ErrUnavailablef("worker %s has reached its memory limit (%d MB allocated, %d MB limit)", nodeID, allocated, *node.MaxMemoryMB)
		}
	}

	if node.MaxCPU != nil {
		allocated, err := p.store.AllocatedCPUByNode(nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+cpuNeeded > *node.MaxCPU {
			return controller.ErrUnavailablef("worker %s has reached its CPU limit (%.1f allocated, %.1f limit)", nodeID, allocated, *node.MaxCPU)
		}
	}

	if node.MaxStorageMB != nil && storageNeeded > 0 {
		allocated, err := p.store.AllocatedStorageByNode(nodeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+storageNeeded > *node.MaxStorageMB {
			return controller.ErrUnavailablef("worker %s has reached its storage limit (%d MB allocated, %d MB limit)", nodeID, allocated, *node.MaxStorageMB)
		}
	}

	return nil
}

// CheckWorkerLimitsExcluding is like CheckWorkerLimits but excludes one gameserver's allocation.
// Used by auto-migration to check if a node can still fit after a resource update.
func (p *PlacementService) CheckWorkerLimitsExcluding(nodeID string, memoryNeeded int, cpuNeeded float64, storageNeeded int, excludeID string) error {
	node, err := p.store.GetWorkerNode(nodeID)
	if err != nil || node == nil {
		return nil
	}

	if node.MaxMemoryMB != nil {
		allocated, err := p.store.AllocatedMemoryByNodeExcluding(nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+memoryNeeded > *node.MaxMemoryMB {
			return controller.ErrUnavailablef("worker %s would exceed memory limit (%d MB allocated + %d MB needed > %d MB limit)", nodeID, allocated, memoryNeeded, *node.MaxMemoryMB)
		}
	}

	if node.MaxCPU != nil {
		allocated, err := p.store.AllocatedCPUByNodeExcluding(nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+cpuNeeded > *node.MaxCPU {
			return controller.ErrUnavailablef("worker %s would exceed CPU limit (%.1f allocated + %.1f needed > %.1f limit)", nodeID, allocated, cpuNeeded, *node.MaxCPU)
		}
	}

	if node.MaxStorageMB != nil && storageNeeded > 0 {
		allocated, err := p.store.AllocatedStorageByNodeExcluding(nodeID, excludeID)
		if err != nil {
			return fmt.Errorf("checking worker limits: %w", err)
		}
		if allocated+storageNeeded > *node.MaxStorageMB {
			return controller.ErrUnavailablef("worker %s would exceed storage limit (%d MB allocated + %d MB needed > %d MB limit)", nodeID, allocated, storageNeeded, *node.MaxStorageMB)
		}
	}

	return nil
}

// AllocatePorts finds a contiguous block of free host ports for the game's port requirements.
func (p *PlacementService) AllocatePorts(game *games.Game, nodeID string, excludeID string) (model.Ports, error) {
	gamePorts := game.DefaultPorts
	if len(gamePorts) == 0 {
		return model.Ports{}, nil
	}

	// Find unique port numbers in order
	seen := make(map[int]bool)
	var uniquePorts []int
	for _, port := range gamePorts {
		if !seen[port.Port] {
			seen[port.Port] = true
			uniquePorts = append(uniquePorts, port.Port)
		}
	}
	sort.Ints(uniquePorts)
	blockSize := len(uniquePorts)

	// Build mapping from original port number to its index (for assignment)
	portIndex := make(map[int]int)
	for i, port := range uniquePorts {
		portIndex[port] = i
	}

	rangeStart, rangeEnd := p.portRangeForNode(nodeID)

	used, err := p.UsedHostPorts(nodeID, excludeID)
	if err != nil {
		return nil, err
	}

	// Find first contiguous block of blockSize free ports.
	// Checks both DB (gamejanitor-managed) and host (net.Listen probe) to avoid
	// conflicts with other Docker instances or services on the host.
	probe := isPortAvailable
	if p.portProbe != nil {
		probe = p.portProbe
	}
	base := -1
	for candidate := rangeStart; candidate+blockSize-1 <= rangeEnd; candidate++ {
		free := true
		for offset := 0; offset < blockSize; offset++ {
			port := candidate + offset
			if used[port] || !probe(port) {
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
	result := make(model.Ports, len(gamePorts))
	for i, gp := range gamePorts {
		allocatedPort := base + portIndex[gp.Port]
		result[i] = model.PortMapping{
			Name:         gp.Name,
			HostPort:     model.FlexInt(allocatedPort),
			InstancePort: model.FlexInt(allocatedPort),
			Protocol:     gp.Protocol,
		}
	}

	p.log.Info("auto-allocated ports", "game", game.ID, "base", base, "block_size", blockSize)

	return result, nil
}
