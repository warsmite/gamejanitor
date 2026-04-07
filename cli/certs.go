package cli

import (
	"fmt"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/utilities/tlsutil"
	"github.com/spf13/cobra"
)

var genWorkerCertCmd = &cobra.Command{
	Use:   "gen-worker-cert <worker-id>",
	Short: "Generate a TLS client certificate for a worker node",
	Args:  cobra.ExactArgs(1),
	RunE:  runGenWorkerCert,
}

func init() {
	genWorkerCertCmd.Flags().StringP("data-dir", "d", config.DefaultConfig().DataDir, "Data directory (must match the controller's data-dir)")
}

func runGenWorkerCert(cmd *cobra.Command, args []string) error {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	workerID := args[0]

	caCert, caKey, err := tlsutil.LoadOrCreateCA(dataDir)
	if err != nil {
		return fmt.Errorf("loading CA: %w", err)
	}

	certPath, keyPath, caPath, err := tlsutil.GenerateWorkerCert(dataDir, workerID, caCert, caKey)
	if err != nil {
		return fmt.Errorf("generating worker cert: %w", err)
	}

	fmt.Println("Worker certificate generated. Copy these files to the worker node:")
	fmt.Println()
	fmt.Printf("  CA cert:     %s\n", caPath)
	fmt.Printf("  Worker cert: %s\n", certPath)
	fmt.Printf("  Worker key:  %s\n", keyPath)
	fmt.Println()
	fmt.Println("On the worker, add to config file:")
	fmt.Println("  tls:")
	fmt.Printf("    ca: %s\n", caPath)
	fmt.Printf("    cert: %s\n", certPath)
	fmt.Printf("    key: %s\n", keyPath)

	return nil
}
