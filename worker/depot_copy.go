package worker

import (
	"fmt"
	"os"
	"os/exec"
)

// CopyDepotToVolume copies depot files into the volume's /server directory
// using cp -a on the host. This avoids the container's cgroup memory limit
// which causes OOM kills when copying large depots (3+ GB) due to page cache.
func CopyDepotToVolume(depotDir string, volumeMountpoint string) error {
	serverDir := volumeMountpoint + "/server"
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("creating server dir: %w", err)
	}

	// cp -a preserves permissions/ownership. The trailing /. copies contents not the dir itself.
	cmd := exec.Command("cp", "-a", depotDir+"/.", serverDir+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying depot to volume: %w (output: %s)", err, string(out))
	}

	// Ensure gameserver user (1001) can read/write
	cmd = exec.Command("chown", "-R", "1001:1001", serverDir)
	cmd.Run() // best-effort

	return nil
}
