package steam

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/warsmite/gamejanitor/steam/proto"
	goproto "google.golang.org/protobuf/proto"
)

// AppInfo contains the resolved depot and manifest information for a Steam app.
type AppInfo struct {
	AppID          uint32
	Depots         []DepotInfo
	BuildID        string
	WorkshopDepotID uint32 // Depot ID for Workshop/UGC content. 0 if not present.
}

// DepotInfo describes a single depot within an app.
type DepotInfo struct {
	DepotID    uint32
	ManifestID uint64
	// DepotFromApp is set when this depot's content comes from a different app (shared depots).
	DepotFromApp uint32
	// OSList is the comma-separated OS filter (e.g. "windows", "linux", "macos").
	// Empty means the depot is platform-independent.
	OSList string
}

// GetAppInfo fetches PICS product info for an app and resolves its depots for the given branch.
func (c *Client) GetAppInfo(ctx context.Context, appID uint32, branch string) (*AppInfo, error) {
	if branch == "" {
		branch = "public"
	}

	// Get access token for the app
	accessToken, err := c.getAppAccessToken(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// Request product info
	header := c.makeHeader()
	body := &proto.CMsgClientPICSProductInfoRequest{
		Apps: []*proto.CMsgClientPICSProductInfoRequest_AppInfo{
			{
				Appid:       &appID,
				AccessToken: &accessToken,
			},
		},
	}

	resp, err := c.SendAndWait(ctx, EMsgClientPICSProductInfoRequest, header, body)
	if err != nil {
		return nil, fmt.Errorf("PICS product info request: %w", err)
	}

	productInfo := &proto.CMsgClientPICSProductInfoResponse{}
	if err := goproto.Unmarshal(resp.Body, productInfo); err != nil {
		return nil, fmt.Errorf("unmarshal product info: %w", err)
	}

	if len(productInfo.GetApps()) == 0 {
		return nil, fmt.Errorf("no product info returned for app %d", appID)
	}

	app := productInfo.GetApps()[0]

	// PICS buffer is text VDF wrapped with a binary header/trailer.
	// Find the text content between the first '"' and trim trailing nulls.
	buf := app.GetBuffer()
	textStart := bytes.IndexByte(buf, '"')
	if textStart < 0 {
		return nil, fmt.Errorf("no text VDF found in PICS response for app %d", appID)
	}
	text := strings.TrimRight(string(buf[textStart:]), "\x00\x08")

	vdf, err := ParseVDF(text)
	if err != nil {
		return nil, fmt.Errorf("parse app VDF: %w", err)
	}

	return parseAppInfo(appID, branch, vdf)
}

// GetDepotDecryptionKey retrieves the AES-256 key for decrypting a depot's content.
func (c *Client) GetDepotDecryptionKey(ctx context.Context, depotID, appID uint32) ([]byte, error) {
	header := c.makeHeader()
	body := &proto.CMsgClientGetDepotDecryptionKey{
		DepotId: &depotID,
		AppId:   &appID,
	}

	resp, err := c.SendAndWait(ctx, EMsgClientGetDepotDecryptionKey, header, body)
	if err != nil {
		return nil, fmt.Errorf("depot decryption key request: %w", err)
	}

	keyResp := &proto.CMsgClientGetDepotDecryptionKeyResponse{}
	if err := goproto.Unmarshal(resp.Body, keyResp); err != nil {
		return nil, fmt.Errorf("unmarshal depot key: %w", err)
	}

	if keyResp.GetEresult() != 1 {
		return nil, fmt.Errorf("depot key request failed with EResult %d", keyResp.GetEresult())
	}

	return keyResp.GetDepotEncryptionKey(), nil
}

func (c *Client) getAppAccessToken(ctx context.Context, appID uint32) (uint64, error) {
	header := c.makeHeader()
	body := &proto.CMsgClientPICSAccessTokenRequest{
		Appids: []uint32{appID},
	}

	resp, err := c.SendAndWait(ctx, EMsgClientPICSAccessTokenRequest, header, body)
	if err != nil {
		return 0, err
	}

	tokenResp := &proto.CMsgClientPICSAccessTokenResponse{}
	if err := goproto.Unmarshal(resp.Body, tokenResp); err != nil {
		return 0, err
	}

	for _, token := range tokenResp.GetAppAccessTokens() {
		if token.GetAppid() == appID {
			return token.GetAccessToken(), nil
		}
	}

	// App might not need an access token (public info)
	return 0, nil
}

func parseAppInfo(appID uint32, branch string, root *VDFNode) (*AppInfo, error) {
	// The VDF structure from PICS is:
	// "appinfo" { "depots" { "<depot_id>" { "manifests" { "<branch>" { "gid" "..." } } } } }
	// Navigate to the root app node, which is the first child.
	var appNode *VDFNode
	if len(root.Children) > 0 {
		appNode = root.Children[0]
	}
	if appNode == nil {
		return nil, fmt.Errorf("no app node in VDF")
	}

	depotsNode := appNode.Child("depots")
	if depotsNode == nil {
		return nil, fmt.Errorf("no depots section in app info")
	}

	info := &AppInfo{AppID: appID}

	// Extract workshop depot ID (used for UGC/Workshop mod downloads)
	if wsDepot := depotsNode.ChildValue("workshopdepot"); wsDepot != "" {
		if id, err := strconv.ParseUint(wsDepot, 10, 32); err == nil {
			info.WorkshopDepotID = uint32(id)
		}
	}

	// Extract build ID from the branch info
	branchesNode := depotsNode.Child("branches")
	if branchesNode != nil {
		branchNode := branchesNode.Child(branch)
		if branchNode != nil {
			info.BuildID = branchNode.ChildValue("buildid")
		}
	}

	// Iterate depot entries. Keys that are numeric are depot IDs.
	for _, depotNode := range depotsNode.Children {
		depotID, err := strconv.ParseUint(depotNode.Key, 10, 32)
		if err != nil {
			continue // skip non-numeric keys like "branches", "baselanguages", etc.
		}

		depot := DepotInfo{
			DepotID: uint32(depotID),
		}

		// Read OS filter from config/oslist
		configNode := depotNode.Child("config")
		if configNode != nil {
			depot.OSList = configNode.ChildValue("oslist")
		}

		// Check for shared depots (depotfromapp)
		if fromApp := depotNode.ChildValue("depotfromapp"); fromApp != "" {
			fa, err := strconv.ParseUint(fromApp, 10, 32)
			if err == nil {
				depot.DepotFromApp = uint32(fa)
			}
		}

		// Get manifest ID for the requested branch
		manifests := depotNode.Child("manifests")
		if manifests == nil {
			continue
		}

		branchManifest := manifests.Child(branch)
		if branchManifest == nil {
			continue
		}

		// The manifest ID can be either a direct value or a child "gid" node.
		gid := branchManifest.ChildValue("gid")
		if gid == "" {
			gid = branchManifest.Value
		}
		if gid == "" {
			continue
		}

		manifestID, err := strconv.ParseUint(gid, 10, 64)
		if err != nil {
			continue
		}
		depot.ManifestID = manifestID

		info.Depots = append(info.Depots, depot)
	}

	if len(info.Depots) == 0 {
		return nil, fmt.Errorf("no depots with manifests found for app %d branch %q", appID, branch)
	}

	return info, nil
}
