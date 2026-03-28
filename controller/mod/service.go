package mod

import (
	"context"
	"log/slog"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// Store abstracts DB operations needed by the mod service.
type Store interface {
	ListInstalledMods(gameserverID string) ([]model.InstalledMod, error)
	GetInstalledMod(id string) (*model.InstalledMod, error)
	GetInstalledModBySource(gameserverID, source, sourceID string) (*model.InstalledMod, error)
	CreateInstalledMod(m *model.InstalledMod) error
	DeleteInstalledMod(id string) error
	GetGameserver(id string) (*model.Gameserver, error)
	ListModsByPackID(gameserverID, packID string) ([]model.InstalledMod, error)
	GetPackExclusions(packModID string) (map[string]bool, error)
	CreatePackExclusion(e *model.PackExclusion) error
	SetModPackID(modID, packID string) error
	UpdateModVersion(modID, versionID, version string) error
}

// FileOperator is a narrow interface for file operations the mod service needs.
type FileOperator interface {
	WriteFile(ctx context.Context, gameserverID string, filePath string, content []byte) error
	DeletePath(ctx context.Context, gameserverID string, targetPath string) error
	CreateDirectory(ctx context.Context, gameserverID string, dirPath string) error
}

type ModService struct {
	catalogs    map[string]ModCatalog
	fileDel     *FileDelivery
	manifestDel *ManifestDelivery
	packDel     *PackDelivery
	store       Store
	fileSvc     FileOperator
	gameStore   *games.GameStore
	broadcaster *controller.EventBus
	log         *slog.Logger
}

func NewModService(store Store, fileSvc FileOperator, gameStore *games.GameStore, broadcaster *controller.EventBus, log *slog.Logger) *ModService {
	return &ModService{
		catalogs:    make(map[string]ModCatalog),
		fileDel:     NewFileDelivery(fileSvc, log),
		manifestDel: NewManifestDelivery(fileSvc, log),
		packDel:     NewPackDelivery(fileSvc, log),
		store:       store,
		fileSvc:     fileSvc,
		gameStore:   gameStore,
		broadcaster: broadcaster,
		log:         log,
	}
}

// RegisterCatalog adds a mod catalog (source) to the service.
func (s *ModService) RegisterCatalog(name string, catalog ModCatalog) {
	s.catalogs[name] = catalog
}

func (s *ModService) ListInstalled(ctx context.Context, gameserverID string) ([]model.InstalledMod, error) {
	return s.store.ListInstalledMods(gameserverID)
}

func (s *ModService) GetSources(ctx context.Context, gameserverID string) ([]ModSourceInfo, error) {
	// TODO: implement with new game YAML categories
	return []ModSourceInfo{}, nil
}

func (s *ModService) Search(ctx context.Context, gameserverID string, source string, query string, offset int, limit int) ([]ModResult, int, error) {
	// TODO: implement with new catalog interface
	return nil, 0, nil
}

func (s *ModService) GetVersions(ctx context.Context, gameserverID string, source string, sourceID string) ([]ModVersion, error) {
	// TODO: implement with new catalog interface
	return nil, nil
}

func (s *ModService) Install(ctx context.Context, gameserverID string, source string, sourceID string, versionID string, name string) (*model.InstalledMod, error) {
	// TODO: implement with new catalog + delivery
	return nil, nil
}

func (s *ModService) Uninstall(ctx context.Context, gameserverID string, modID string) error {
	mod, err := s.store.GetInstalledMod(modID)
	if err != nil {
		return err
	}
	if mod == nil {
		return controller.ErrNotFound("mod not found")
	}
	if mod.GameserverID != gameserverID {
		return controller.ErrNotFound("mod not found")
	}
	return s.store.DeleteInstalledMod(modID)
}
