package sftp

import (
	"context"
	"fmt"

	pb "github.com/warsmite/gamejanitor/worker/proto"
)

// RemoteAuth validates SFTP credentials by calling back to the controller via gRPC.
// Used on worker nodes.
type RemoteAuth struct {
	client pb.ControllerServiceClient
}

func NewRemoteAuth(client pb.ControllerServiceClient) *RemoteAuth {
	return &RemoteAuth{client: client}
}

func (a *RemoteAuth) ValidateLogin(username, password string) (string, string, error) {
	resp, err := a.client.ValidateSFTPLogin(context.Background(), &pb.SFTPLoginRequest{
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", "", fmt.Errorf("sftp auth callback failed: %w", err)
	}
	if !resp.Valid {
		return "", "", fmt.Errorf("invalid credentials")
	}
	return resp.GameserverId, resp.VolumeName, nil
}
