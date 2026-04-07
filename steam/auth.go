package steam

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/warsmite/gamejanitor/steam/proto"
	goproto "google.golang.org/protobuf/proto"
)

// LoginWithRefreshToken authenticates to Steam using a previously obtained refresh token.
// This is the primary login path for automated use.
func (c *Client) LoginWithRefreshToken(ctx context.Context, accountName, refreshToken string) error {
	// Send ClientHello first
	if err := c.sendClientHello(); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Build logon message. The refresh token goes into the access_token field
	// per Steam's protocol convention. When using a refresh token, most fields
	// are omitted — account_name, machine_name, etc. are NOT sent.
	ver := uint32(protocolVersion)
	clientOSType := uint32(16) // Windows 10
	cellID := uint32(0)
	clientPkgVersion := uint32(1771)
	steam2TicketRequest := false
	supportsRateLimit := false

	// Obfuscated private IP — required for CM session.
	// The obfuscation XORs the IP with 0xBAADF00D.
	obfuscatedIP := uint32(0x0100007f) ^ 0xBAADF00D // 127.0.0.1 XOR'd
	privateIP := &proto.CMsgIPAddress{
		Ip: &proto.CMsgIPAddress_V4{V4: obfuscatedIP},
	}

	steamID := NewIndividualSteamID(0).Uint64()
	sessionID := int32(0)

	logon := &proto.CMsgClientLogon{
		ProtocolVersion:                 &ver,
		ClientOsType:                    &clientOSType,
		CellId:                          &cellID,
		AccountName:                     &accountName,
		AccessToken:                     &refreshToken,
		ClientPackageVersion:            &clientPkgVersion,
		SupportsRateLimitResponse:       &supportsRateLimit,
		Steam2TicketRequest:             &steam2TicketRequest,
		ObfuscatedPrivateIp:             privateIP,
		DeprecatedObfustucatedPrivateIp: &obfuscatedIP,
	}

	header := &proto.CMsgProtoBufHeader{
		Steamid:         &steamID,
		ClientSessionid: &sessionID,
	}

	// Register waiter before sending — logon response is dispatched by EMsg, not job ID.
	responseCh := c.WaitForEMsg(EMsgClientLogOnResponse)

	if err := c.Send(EMsgClientLogon, header, logon); err != nil {
		return fmt.Errorf("send logon: %w", err)
	}

	select {
	case resp := <-responseCh:
		return c.handleLogonResponse(resp)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LoginAnonymous authenticates as an anonymous user for downloading free content.
func (c *Client) LoginAnonymous(ctx context.Context) error {
	if err := c.sendClientHello(); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	protocolVersion := uint32(protocolVersion)
	clientOSType := uint32(16)
	clientLang := "english"
	cellID := uint32(0)
	clientPkgVersion := uint32(1771)
	machineName := "gamejanitor"

	steamID := NewAnonymousSteamID().Uint64()
	sessionID := int32(0)

	logon := &proto.CMsgClientLogon{
		ProtocolVersion:      &protocolVersion,
		ClientOsType:        &clientOSType,
		ClientLanguage:      &clientLang,
		CellId:              &cellID,
		ClientPackageVersion: &clientPkgVersion,
		MachineName:         &machineName,
	}

	header := &proto.CMsgProtoBufHeader{
		Steamid:         &steamID,
		ClientSessionid: &sessionID,
	}

	responseCh := c.WaitForEMsg(EMsgClientLogOnResponse)

	if err := c.Send(EMsgClientLogon, header, logon); err != nil {
		return fmt.Errorf("send logon: %w", err)
	}

	select {
	case resp := <-responseCh:
		return c.handleLogonResponse(resp)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BeginAuthSessionViaCredentials starts the username/password login flow.
// Returns a session that must be completed with a Steam Guard code.
type AuthSession struct {
	ClientID             uint64
	RequestID            []byte
	AllowedConfirmations []AuthConfirmationType
	Interval             float32
	client               *Client
	steamID              SteamID
}

type AuthConfirmationType int32

const (
	AuthConfirmNone               AuthConfirmationType = 0
	AuthConfirmEmailCode          AuthConfirmationType = 2
	AuthConfirmDeviceCode         AuthConfirmationType = 3 // TOTP from authenticator app
	AuthConfirmDeviceConfirmation AuthConfirmationType = 4 // Approve/deny on Steam mobile app
	AuthConfirmEmailConfirmation  AuthConfirmationType = 5
)

// BeginAuthViaCredentials starts an interactive login. After calling this,
// submit the Steam Guard code with SubmitSteamGuardCode, then poll with PollAuthStatus.
func (c *Client) BeginAuthViaCredentials(ctx context.Context, accountName, password string) (*AuthSession, error) {
	if err := c.sendClientHello(); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Get RSA public key for password encryption
	rsaResp, err := c.SendServiceMethod(ctx, "Authentication.GetPasswordRSAPublicKey#1", &proto.CAuthentication_GetPasswordRSAPublicKey_Request{
		AccountName: &accountName,
	}, false)
	if err != nil {
		return nil, fmt.Errorf("get RSA key: %w", err)
	}

	rsaKey := &proto.CAuthentication_GetPasswordRSAPublicKey_Response{}
	if err := goproto.Unmarshal(rsaResp.Body, rsaKey); err != nil {
		return nil, fmt.Errorf("unmarshal RSA key: %w", err)
	}

	// Encrypt password with RSA public key
	encryptedPassword, err := encryptPasswordRSA(password, rsaKey.GetPublickeyMod(), rsaKey.GetPublickeyExp())
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}

	encPasswordB64 := base64.StdEncoding.EncodeToString(encryptedPassword)
	keyTimestamp := rsaKey.GetTimestamp()

	websiteID := "Client"
	persistence := proto.ESessionPersistence_k_ESessionPersistence_Persistent.Enum()
	platformType := proto.EAuthTokenPlatformType_k_EAuthTokenPlatformType_SteamClient.Enum()
	deviceFriendlyName := "gamejanitor"

	beginResp, err := c.SendServiceMethod(ctx, "Authentication.BeginAuthSessionViaCredentials#1", &proto.CAuthentication_BeginAuthSessionViaCredentials_Request{
		AccountName:        &accountName,
		EncryptedPassword:  &encPasswordB64,
		EncryptionTimestamp: &keyTimestamp,
		Persistence:        persistence,
		PlatformType:       platformType,
		WebsiteId:          &websiteID,
		DeviceFriendlyName: &deviceFriendlyName,
	}, false)
	if err != nil {
		return nil, fmt.Errorf("begin auth: %w", err)
	}

	resp := &proto.CAuthentication_BeginAuthSessionViaCredentials_Response{}
	if err := goproto.Unmarshal(beginResp.Body, resp); err != nil {
		return nil, fmt.Errorf("unmarshal begin auth: %w", err)
	}

	session := &AuthSession{
		ClientID:  resp.GetClientId(),
		RequestID: resp.GetRequestId(),
		Interval:  resp.GetInterval(),
		client:    c,
		steamID:   SteamID(resp.GetSteamid()),
	}

	for _, conf := range resp.GetAllowedConfirmations() {
		session.AllowedConfirmations = append(session.AllowedConfirmations, AuthConfirmationType(conf.GetConfirmationType()))
	}

	return session, nil
}

// SubmitSteamGuardCode submits a Steam Guard code (email or TOTP) for the auth session.
func (s *AuthSession) SubmitSteamGuardCode(ctx context.Context, code string, codeType AuthConfirmationType) error {
	guardType := proto.EAuthSessionGuardType(codeType)

	_, err := s.client.SendServiceMethod(ctx, "Authentication.UpdateAuthSessionWithSteamGuardCode#1", &proto.CAuthentication_UpdateAuthSessionWithSteamGuardCode_Request{
		ClientId: &s.ClientID,
		Steamid:  goproto.Uint64(s.steamID.Uint64()),
		Code:     &code,
		CodeType: &guardType,
	}, false)
	return err
}

// PollAuthStatus polls for the auth session to complete. Returns refresh and access tokens on success.
func (s *AuthSession) PollAuthStatus(ctx context.Context) (refreshToken, accessToken string, err error) {
	interval := time.Duration(s.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
		}

		resp, err := s.client.SendServiceMethod(ctx, "Authentication.PollAuthSessionStatus#1", &proto.CAuthentication_PollAuthSessionStatus_Request{
			ClientId:  &s.ClientID,
			RequestId: s.RequestID,
		}, false)
		if err != nil {
			return "", "", fmt.Errorf("poll auth status: %w", err)
		}

		pollResp := &proto.CAuthentication_PollAuthSessionStatus_Response{}
		if err := goproto.Unmarshal(resp.Body, pollResp); err != nil {
			return "", "", fmt.Errorf("unmarshal poll response: %w", err)
		}

		if pollResp.GetRefreshToken() != "" {
			return pollResp.GetRefreshToken(), pollResp.GetAccessToken(), nil
		}

		if pollResp.GetHadRemoteInteraction() {
			s.client.log.Debug("waiting for Steam Guard confirmation...")
		}
	}
}

func (c *Client) sendClientHello() error {
	ver := uint32(protocolVersion)
	header := &proto.CMsgProtoBufHeader{}
	body := &proto.CMsgClientHello{
		ProtocolVersion: &ver,
	}
	return c.Send(EMsgClientHello, header, body)
}

func (c *Client) handleLogonResponse(msg *Message) error {
	if msg.EMsg != EMsgClientLogOnResponse {
		return fmt.Errorf("expected ClientLogOnResponse (751), got %d", msg.EMsg)
	}

	resp := &proto.CMsgClientLogonResponse{}
	if err := goproto.Unmarshal(msg.Body, resp); err != nil {
		return fmt.Errorf("unmarshal logon response: %w", err)
	}

	eresult := resp.GetEresult()
	if eresult != 1 { // EResult_OK = 1
		return fmt.Errorf("logon failed with EResult %d", eresult)
	}

	c.steamID.Store(msg.Header.GetSteamid())
	c.sessionID.Store(msg.Header.GetClientSessionid())

	heartbeatSec := resp.GetHeartbeatSeconds()
	if heartbeatSec > 0 {
		c.startHeartbeat(time.Duration(heartbeatSec) * time.Second)
	}

	c.log.Info("logged in to Steam",
		"steam_id", c.steamID.Load(),
		"cell_id", resp.GetCellId(),
		"heartbeat_sec", heartbeatSec,
	)

	return nil
}

// encryptPasswordRSA encrypts a password with the Steam RSA public key.
// The modulus and exponent are hex strings from the GetPasswordRSAPublicKey response.
func encryptPasswordRSA(password, modulusHex, exponentHex string) ([]byte, error) {
	modulus := new(big.Int)
	if _, ok := modulus.SetString(modulusHex, 16); !ok {
		return nil, fmt.Errorf("invalid RSA modulus")
	}

	exponent := new(big.Int)
	if _, ok := exponent.SetString(exponentHex, 16); !ok {
		return nil, fmt.Errorf("invalid RSA exponent")
	}

	pubKey := &rsa.PublicKey{
		N: modulus,
		E: int(exponent.Int64()),
	}

	return rsa.EncryptPKCS1v15(rand.Reader, pubKey, []byte(password))
}
