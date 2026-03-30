package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/warsmite/gamejanitor/steam"
	"golang.org/x/term"
)

var steamCmd = &cobra.Command{
	Use:   "steam",
	Short: "Manage Steam account for authenticated game downloads",
}

func init() {
	steamCmd.AddCommand(steamLoginCmd)
	steamCmd.AddCommand(steamLogoutCmd)
	steamCmd.AddCommand(steamStatusCmd)
}

var steamLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Link a Steam account for downloading games that require authentication",
	Long: `Authenticates with Steam and stores a refresh token for downloading game
servers that require a Steam account (e.g. Project Zomboid, Valheim).

The token is valid for ~200 days. Gamejanitor will warn when it's about to expire.
No password is stored — only a revocable refresh token.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		reader := bufio.NewReader(os.Stdin)

		fmt.Print("Steam username: ")
		username, _ := reader.ReadString('\n')
		username = strings.TrimSpace(username)
		if username == "" {
			return exitError(fmt.Errorf("username cannot be empty"))
		}

		fmt.Print("Password: ")
		passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return exitError(fmt.Errorf("failed to read password: %w", err))
		}
		password := string(passwordBytes)

		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

		client := steam.NewClient(log)
		loginCtx, cancel := context.WithTimeout(ctx(), 30*time.Second)
		defer cancel()

		if err := client.Connect(loginCtx); err != nil {
			return exitError(fmt.Errorf("failed to connect to Steam: %w", err))
		}
		defer client.Close()

		session, err := client.BeginAuthViaCredentials(loginCtx, username, password)
		if err != nil {
			return exitError(fmt.Errorf("authentication failed: %w", err))
		}

		// Determine what type of confirmation is needed.
		// Prefer device confirmation (approve on phone) over TOTP code entry.
		var confirmType steam.AuthConfirmationType
		for _, ct := range session.AllowedConfirmations {
			if ct == steam.AuthConfirmDeviceConfirmation {
				confirmType = ct
				break
			}
			if ct == steam.AuthConfirmDeviceCode || ct == steam.AuthConfirmEmailCode {
				confirmType = ct
			}
		}

		switch confirmType {
		case steam.AuthConfirmDeviceConfirmation:
			fmt.Println("Check the Steam app on your phone — approve the login request.")
		case steam.AuthConfirmDeviceCode:
			fmt.Print("Steam Guard code (from authenticator app): ")
			codeLine, _ := reader.ReadString('\n')
			code := strings.TrimSpace(codeLine)
			if code == "" {
				return exitError(fmt.Errorf("Steam Guard code cannot be empty"))
			}
			if err := session.SubmitSteamGuardCode(loginCtx, code, confirmType); err != nil {
				return exitError(fmt.Errorf("Steam Guard verification failed: %w", err))
			}
		case steam.AuthConfirmEmailCode:
			fmt.Print("Steam Guard code (check your email): ")
			codeLine, _ := reader.ReadString('\n')
			code := strings.TrimSpace(codeLine)
			if code == "" {
				return exitError(fmt.Errorf("Steam Guard code cannot be empty"))
			}
			if err := session.SubmitSteamGuardCode(loginCtx, code, confirmType); err != nil {
				return exitError(fmt.Errorf("Steam Guard verification failed: %w", err))
			}
		}

		// Poll for completion
		pollCtx, pollCancel := context.WithTimeout(ctx(), 2*time.Minute)
		defer pollCancel()

		fmt.Println("Waiting for authentication...")

		refreshToken, _, err := session.PollAuthStatus(pollCtx)
		if err != nil {
			return exitError(fmt.Errorf("authentication failed: %w", err))
		}

		// Store via the API settings endpoint
		usernameJSON, _ := json.Marshal(username)
		tokenJSON, _ := json.Marshal(refreshToken)
		sdkClient := getClient()
		if err := sdkClient.Settings.Update(ctx(), map[string]json.RawMessage{
			"steam_account_name":  usernameJSON,
			"steam_refresh_token": tokenJSON,
		}); err != nil {
			return exitError(fmt.Errorf("failed to save Steam credentials: %w", err))
		}

		fmt.Printf("Steam account %q linked successfully.\n", username)
		fmt.Println("Token is valid for ~200 days. Gamejanitor will warn before expiry.")
		return nil
	},
}

var steamLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the linked Steam account",
	RunE: func(cmd *cobra.Command, args []string) error {
		sdkClient := getClient()

		emptyJSON, _ := json.Marshal("")
		if err := sdkClient.Settings.Update(ctx(), map[string]json.RawMessage{
			"steam_refresh_token": emptyJSON,
			"steam_account_name":  emptyJSON,
		}); err != nil {
			return exitError(fmt.Errorf("failed to clear Steam credentials: %w", err))
		}

		fmt.Println("Steam account unlinked.")
		return nil
	},
}

var steamStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show linked Steam account status",
	RunE: func(cmd *cobra.Command, args []string) error {
		sdkClient := getClient()
		settings, err := sdkClient.Settings.Get(ctx())
		if err != nil {
			return exitError(err)
		}

		var accountName string
		if raw, ok := settings["steam_account_name"]; ok {
			json.Unmarshal(raw, &accountName)
		}
		var refreshToken string
		if raw, ok := settings["steam_refresh_token"]; ok {
			json.Unmarshal(raw, &refreshToken)
		}

		if accountName == "" || refreshToken == "" {
			fmt.Println("No Steam account linked.")
			fmt.Println("Run 'gamejanitor steam login' to link an account.")
			return nil
		}

		fmt.Printf("Linked account: %s\n", accountName)
		fmt.Println("Refresh token: present")
		return nil
	},
}
