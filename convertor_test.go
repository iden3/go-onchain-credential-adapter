package convertor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/iden3/go-iden3-core/v2/w3c"
	"github.com/stretchr/testify/require"
)

func TestConvertorHex(t *testing.T) {
	issuerDID, err := w3c.ParseDID("did:polygonid:polygon:mumbai:2qCU58EJgrEMEAwt9YxySaG8iEps6Nwqy3a5Fg6e71")
	require.NoError(t, err)
	hexdata := "0x00000000000000000000000000000000000000000000000000000000000001400000000000000000000000000000000a6ff88524b07a4eaab4d733c595172ff5000d60ad1dea10b097b1ab7638b962383c12d17575437e63c9786fff897f12020000000000000000000000006ae7e07c8763c284b7c91371f934e46c766d0ec600000000000000000000000000000000000000000000000018be19ab7b5a636b000000000000000000000000000000000000000065f1a59700000000000000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000420000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000001c00000000000000000000000000000000000000000000000000000000065ca189700000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000a368747470733a2f2f676973742e67697468756275736572636f6e74656e742e636f6d2f696c79612d6b6f726f7479612f36363034393663383539663864333161376432613932636135653937303936372f7261772f366235666331346665363330633137626661353265303565303866646338333934633565613063652f6e6f6e2d6d65726b6c697a65642d6e6f6e2d7a65726f2d62616c616e63652e6a736f6e6c640000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000742616c616e63650000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000a168747470733a2f2f676973742e67697468756275736572636f6e74656e742e636f6d2f696c79612d6b6f726f7479612f65313063643739613863633236616236653430343030613131383338363137652f7261772f353735656463333364343835653261346338303662616164393765323131313766336339306139662f6e6f6e2d6d65726b6c697a65642d6e6f6e2d7a65726f2d62616c616e63652e6a736f6e00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000018be19ab7b5a636b00000000000000000000000000000000000000000000000000000000000000a0000000000000000000000000000000000000000000000000000000000000000762616c616e636500000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000006ae7e07c8763c284b7c91371f934e46c766d0ec600000000000000000000000000000000000000000000000000000000000000a0000000000000000000000000000000000000000000000000000000000000000761646472657373000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	ethcli, err := ethclient.Dial("https://polygon-mumbai.g.alchemy.com/v2/6S0RiH55rrmlnrkMiEm0IL2Zy4O-VrnQ")
	require.NoError(t, err)
	w3cCred, err := W3CCredentialFromOnchainHex(
		context.Background(),
		ethcli,
		issuerDID,
		hexdata,
	)
	require.NoError(t, err)
	bytesCred, err := json.Marshal(w3cCred)
	require.NoError(t, err)
	fmt.Println(string(bytesCred))
}
