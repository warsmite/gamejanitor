package steam

// EMsg represents a Steam message type identifier.
type EMsg uint32

const (
	EMsgMulti                              EMsg = 1
	EMsgServiceMethodResponse              EMsg = 146
	EMsgServiceMethod                      EMsg = 147
	EMsgServiceMethodCallFromClient        EMsg = 151
	EMsgServiceMethodSendToClient          EMsg = 152
	EMsgChannelEncryptRequest              EMsg = 1303
	EMsgChannelEncryptResponse             EMsg = 1304
	EMsgChannelEncryptResult               EMsg = 1305
	EMsgClientHeartBeat                    EMsg = 703
	EMsgClientLogOnResponse                EMsg = 751
	EMsgClientLoggedOff                    EMsg = 761
	EMsgClientGetDepotDecryptionKey        EMsg = 5438
	EMsgClientGetDepotDecryptionKeyResponse EMsg = 5439
	EMsgClientLogon                        EMsg = 5514
	EMsgClientPICSProductInfoRequest       EMsg = 8903
	EMsgClientPICSProductInfoResponse      EMsg = 8904
	EMsgClientPICSAccessTokenRequest       EMsg = 8905
	EMsgClientPICSAccessTokenResponse      EMsg = 8906
	EMsgServiceMethodCallFromClientNonAuthed EMsg = 9804
	EMsgClientHello                        EMsg = 9805
)

const eMsgProtoMask EMsg = 0x80000000

func (e EMsg) IsProto() bool {
	return e&eMsgProtoMask != 0
}

func (e EMsg) Value() EMsg {
	return e & ^eMsgProtoMask
}
