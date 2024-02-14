package convertor

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	core "github.com/iden3/go-iden3-core/v2"
	"github.com/iden3/go-iden3-core/v2/w3c"
	"github.com/iden3/go-merkletree-sql/v2"
	contractABI "github.com/iden3/go-onchain-credential-adapter/internal"
	"github.com/iden3/go-schema-processor/v2/verifiable"
)

type adapter struct {
	onchainCli *contractABI.OnchainIssuer
	did        *w3c.DID

	id      core.ID
	address string
	chainID uint64
}

func newAdapter(
	_ context.Context,
	ethcli *ethclient.Client,
	did *w3c.DID,
) (*adapter, error) {
	id, err := core.IDFromDID(*did)
	if err != nil {
		return nil, fmt.Errorf("failed to extract issuerID from issuerDID '%s': %w", did, err)
	}
	contractAddressHex, err := core.EthAddressFromID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to extract contract address from issuerID '%s': %w", id, err)
	}
	chainID, err := core.ChainIDfromDID(*did)
	if err != nil {
		return nil, fmt.Errorf("failed to extract chainID from issuerDID '%s': %w", did, err)
	}

	onchainCli, err := contractABI.NewOnchainIssuer(contractAddressHex, ethcli)
	if err != nil {
		return nil, fmt.Errorf("failed to create onchain issuer client: %w", err)
	}

	return &adapter{
		onchainCli: onchainCli,
		did:        did,
		id:         id,
		address:    common.BytesToAddress(contractAddressHex[:]).Hex(),
		chainID:    uint64(chainID),
	}, nil
}

func (a *adapter) credentialStatus(revNonce uint64) *verifiable.CredentialStatus {
	id := fmt.Sprintf("%s/credentialStatus?revocationNonce=%d&contractAddress=%d:%s",
		a.did.String(), revNonce, a.chainID, a.address)
	return &verifiable.CredentialStatus{
		ID:              id,
		Type:            verifiable.Iden3OnchainSparseMerkleTreeProof2023,
		RevocationNonce: revNonce,
	}
}

func (a *adapter) credentialID(id *big.Int) string {
	return fmt.Sprintf("urn:iden3:onchain:%d:%s:%s", a.chainID, a.address, id.String())
}

func (a *adapter) existenceProof(ctx context.Context, coreClaim *core.Claim) (*verifiable.Iden3SparseMerkleTreeProof, error) {
	hindex, err := coreClaim.HIndex()
	if err != nil {
		return nil,
			fmt.Errorf("failed to get hash index from core claim: %w", err)
	}

	mtpProof, stateInfo, err := a.onchainCli.GetClaimProofWithStateInfo(
		&bind.CallOpts{Context: ctx}, hindex)
	if err != nil {
		return nil,
			fmt.Errorf("failed to get claim proof for hash index '%s': %w", hindex, err)
	}
	if !mtpProof.Existence {
		return nil,
			fmt.Errorf("the hash index '%s' does not exist in the issuer state", hindex)
	}

	latestStateHash, err := merkletree.NewHashFromBigInt(stateInfo.State)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create hash for latest state '%s': %w", stateInfo.State, err)
	}
	latestClaimsOfRootHash, err := merkletree.NewHashFromBigInt(stateInfo.ClaimsRoot)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create hash for latest claims root '%s': %w", stateInfo.ClaimsRoot, err)
	}
	latestRevocationOfRootHash, err := merkletree.NewHashFromBigInt(stateInfo.RevocationsRoot)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create hash for latest revocation root '%s': %w", stateInfo.RevocationsRoot, err)
	}
	latestRootsOfRootHash, err := merkletree.NewHashFromBigInt(stateInfo.RootsRoot)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create hash for latest root of roots root '%s': %w", stateInfo.RootsRoot, err)
	}

	coreClaimHex, err := coreClaim.Hex()
	if err != nil {
		return nil,
			fmt.Errorf("failed to convert core claim to hex: %w", err)
	}

	issuerData := verifiable.IssuerData{
		ID: a.did.String(),
		State: verifiable.State{
			Value:              strPtr(latestStateHash.Hex()),
			ClaimsTreeRoot:     strPtr(latestClaimsOfRootHash.Hex()),
			RevocationTreeRoot: strPtr(latestRevocationOfRootHash.Hex()),
			RootOfRoots:        strPtr(latestRootsOfRootHash.Hex()),
		},
	}

	p, err := convertChainProofToMerkleProof(mtpProof)
	if err != nil {
		return nil,
			fmt.Errorf("failed to convert chain proof to merkle proof: %w", err)
	}

	return &verifiable.Iden3SparseMerkleTreeProof{
		Type:       verifiable.Iden3SparseMerkleTreeProofType,
		CoreClaim:  coreClaimHex,
		IssuerData: issuerData,
		MTP:        p,
	}, nil
}

func (a *adapter) fetchOnchainDataForSubject(
	ctx context.Context,
	subjectID *big.Int,
	credentialID *big.Int,
) (
	contractABI.INonMerklizedIssuerCredentialData,
	[8]*big.Int,
	[]contractABI.INonMerklizedIssuerSubjectField,
	error,
) {
	return a.onchainCli.GetCredential(&bind.CallOpts{Context: ctx}, subjectID, credentialID)
}

func (a *adapter) unpackHexToStructs(credentialHex string) (
	out0 contractABI.INonMerklizedIssuerCredentialData,
	out1 [8]*big.Int,
	out2 []contractABI.INonMerklizedIssuerSubjectField,
	err error,
) {
	credentialHex = strings.TrimPrefix(credentialHex, "0x")
	credentialHex = strings.TrimPrefix(credentialHex, "0X")
	hexBytes, err := hex.DecodeString(credentialHex)
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to decode hex '%s': %w", credentialHex, err)
	}
	onchainABI, err := contractABI.OnchainIssuerMetaData.GetAbi()
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to get ABI: %w", err)
	}
	out, err := onchainABI.Unpack("getCredential", hexBytes)
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to unpack hex '%s': %w", credentialHex, err)
	}

	out0 = *abi.ConvertType(out[0], new(contractABI.INonMerklizedIssuerCredentialData)).(*contractABI.INonMerklizedIssuerCredentialData)
	out1 = *abi.ConvertType(out[1], new([8]*big.Int)).(*[8]*big.Int)
	out2 = *abi.ConvertType(out[2], new([]contractABI.INonMerklizedIssuerSubjectField)).(*[]contractABI.INonMerklizedIssuerSubjectField)
	return out0, out1, out2, nil
}

func (a *adapter) credentialProtocolVersion(ctx context.Context) (string, error) {
	v, err := a.onchainCli.GetCredentialProtocolVersion(&bind.CallOpts{Context: ctx})
	if err != nil {
		return "", fmt.Errorf("failed to get credential protocol version: %w", err)
	}
	return v, nil
}

func strPtr(s string) *string {
	return &s
}

func convertChainProofToMerkleProof(proof contractABI.SmtLibProof) (*merkletree.Proof, error) {
	var (
		existence bool
		nodeAux   *merkletree.NodeAux
		err       error
	)

	if proof.Existence {
		existence = true
	} else {
		existence = false
		if proof.AuxExistence {
			nodeAux = &merkletree.NodeAux{}
			nodeAux.Key, err = merkletree.NewHashFromBigInt(proof.AuxIndex)
			if err != nil {
				return nil,
					fmt.Errorf("failed to create hash for AuxIndex '%s': %w", proof.AuxIndex, err)
			}
			nodeAux.Value, err = merkletree.NewHashFromBigInt(proof.AuxValue)
			if err != nil {
				return nil,
					fmt.Errorf("failed to create hash for AuxValue '%s': %w", proof.AuxValue, err)
			}
		}
	}

	allSiblings := make([]*merkletree.Hash, len(proof.Siblings))
	for i, s := range proof.Siblings {
		var sh *merkletree.Hash
		sh, err = merkletree.NewHashFromBigInt(s)
		if err != nil {
			return nil,
				fmt.Errorf("failed to create hash for sibling '%s': %w", s, err)
		}
		allSiblings[i] = sh
	}

	p, err := merkletree.NewProofFromData(existence, allSiblings, nodeAux)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create merkle proof: %w", err)
	}

	return p, nil
}
