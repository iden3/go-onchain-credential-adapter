package convertor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	core "github.com/iden3/go-iden3-core/v2"
	"github.com/iden3/go-iden3-core/v2/w3c"
	"github.com/iden3/go-merkletree-sql/v2"
	"github.com/iden3/go-schema-processor/v2/merklize"
	"github.com/iden3/go-schema-processor/v2/verifiable"
	"github.com/piprate/json-gold/ld"
)

const (
	booleanHashTrue  = "18586133768512220936620570745912940619677854269274689475585506675881198879027"
	booleanHashFalse = "19014214495641488759237505126948346942972912379615652741039992445865937985820"
)

// Convertor converts onchain issuer data to W3C verifiable credentials.
type Convertor struct {
	marklizeInstance merklize.Options
}

// ConvertorOption is a functional option for Convertor.
type ConvertorOption func(*Convertor)

// WithMerklizerOptions sets options for merklizer.
func WithMerklizerOptions(m merklize.Options) ConvertorOption {
	return func(p *Convertor) {
		p.marklizeInstance = m
	}
}

// NewConvertor creates a new Convertor.
func NewConvertor(opts ...ConvertorOption) *Convertor {
	p := &Convertor{}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

func ConvertCredential(issuerDID *w3c.DID, hexStr string) (*verifiable.W3CCredential, error) {
	return (&Convertor{}).ConvertCredential(issuerDID, hexStr)
}

func (p *Convertor) ConvertCredential(issuerDID *w3c.DID, credentialHex string) (*verifiable.W3CCredential, error) {
	credentialData, coreClaimBigInts, credentialSubjectFields, err := unpackHexToSturcts(credentialHex)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack hex to structs: %w", err)
	}
	coreClaim, err := core.NewClaimFromBigInts(coreClaimBigInts)
	if err != nil {
		return nil, fmt.Errorf("failed to create core claim: %w", err)
	}

	var expirationTime *time.Time
	expT, ok := coreClaim.GetExpirationDate()
	if ok {
		e := expT.UTC()
		expirationTime = &e
	}
	issuanceTime := time.Unix(int64(credentialData.IssuanceDate), 0).UTC()

	credentialStatus, err := credentialStatus(issuerDID, coreClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to create credential status: %w", err)
	}

	credentialSubject, err := p.convertCredentialSubject(
		coreClaim,
		credentialData.Context,
		credentialData.Type,
		credentialSubjectFields,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert credential subject: %w", err)
	}

	credentialID, contractAddress, err := convertCredentialID(issuerDID, credentialData.Id.String())
	if err != nil {
		return nil, err
	}

	cli, err := ethclient.Dial("https://polygon-mumbai.g.alchemy.com/v2/6S0RiH55rrmlnrkMiEm0IL2Zy4O-VrnQ")
	if err != nil {
		return nil, err
	}
	onchainIssuerClient, err := NewOnchainIssuer(common.HexToAddress(contractAddress), cli)
	if err != nil {
		return nil, err
	}

	proof, err := extractMTPProof(context.Background(), onchainIssuerClient, coreClaim, issuerDID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract MTP proof: %w", err)
	}

	allContexts := []string{
		verifiable.JSONLDSchemaIden3Credential,
		verifiable.JSONLDSchemaW3CCredential2018,
	}
	allContexts = append(allContexts, credentialData.Context...)
	return &verifiable.W3CCredential{
		ID:      credentialID,
		Context: allContexts,
		Type: []string{
			verifiable.TypeW3CVerifiableCredential,
			credentialData.Type,
		},
		CredentialSchema: verifiable.CredentialSchema{
			ID:   credentialData.CredentialSchema,
			Type: "JsonSchema2023",
		},
		Expiration:        expirationTime,
		IssuanceDate:      &issuanceTime,
		Issuer:            issuerDID.String(),
		CredentialStatus:  credentialStatus,
		CredentialSubject: credentialSubject,
		Proof:             verifiable.CredentialProofs{proof},
	}, nil
}

func (p *Convertor) convertCredentialSubject(
	coreClaim *core.Claim,
	chainContexts []string,
	credentialType string,
	credentialSubjectFields []NonMerklizedIssuerLibSubjectField,
) (map[string]any, error) {
	contexts := []string{
		verifiable.JSONLDSchemaW3CCredential2018,
		verifiable.JSONLDSchemaIden3Credential,
	}
	contexts = append(contexts, chainContexts...)
	contextbytes, err := json.Marshal(map[string][]string{
		"@context": contexts,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal context: %w", err)
	}

	credentialSubject := make(map[string]any)
	for _, f := range credentialSubjectFields {
		datatype, err := p.marklizeInstance.TypeFromContext(
			contextbytes,
			fmt.Sprintf("%s.%s", credentialType, f.Key),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to extract type for field '%s': %w", f.Key, err)
		}

		switch datatype {
		case ld.XSDBoolean:
			switch f.Value.String() {
			case booleanHashTrue:
				credentialSubject[f.Key] = true
			case booleanHashFalse:
				credentialSubject[f.Key] = false
			default:
				return nil, fmt.Errorf("unsupported boolean value: %s", f.Value)
			}
		case ld.XSDNS + "positiveInteger",
			ld.XSDNS + "nonNegativeInteger",
			ld.XSDNS + "negativeInteger",
			ld.XSDNS + "nonPositiveInteger":
			credentialSubject[f.Key] = f.Value.String()
		case ld.XSDInteger:
			credentialSubject[f.Key] = f.Value.Int64()
		case ld.XSDString:
			source := string(f.RawValue)
			if err := validateSourceValue(datatype, f.Value, source); err != nil {
				return nil, err
			}
			credentialSubject[f.Key] = source
		case ld.XSDNS + "dateTime":
			timestamp := big.NewInt(0).SetBytes(f.RawValue)
			sourceTime := time.Unix(
				timestamp.Int64(),
				0,
			).UTC().Format(time.RFC3339Nano)
			if err := validateSourceValue(datatype, f.Value, sourceTime); err != nil {
				return nil, err
			}
			credentialSubject[f.Key] = sourceTime
		case ld.XSDDouble:
			v, _, err := big.NewFloat(0).Parse(
				hex.EncodeToString(f.RawValue), 16)
			if err != nil {
				return nil,
					fmt.Errorf("failed to convert string '%s' to float", f.RawValue)
			}
			sourceDouble, _ := v.Float64()
			if err := validateSourceValue(datatype, f.Value, sourceDouble); err != nil {
				return nil, err
			}
			credentialSubject[f.Key] = sourceDouble
		default:
			return nil, fmt.Errorf("unsupported type: %s", datatype)
		}
	}
	// TODO (ilya-korotia): test for self credential
	subjectID, err := coreClaim.GetID()
	if err != nil {
		return nil, fmt.Errorf("failed to get ID from core claim: %w", err)
	}
	subjectDID, err := core.ParseDIDFromID(subjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ID to DID: %w", err)
	}
	credentialSubject["id"] = subjectDID.String()
	if err != nil {
		return nil, fmt.Errorf("failed to get ID from core claim: %w", err)
	}
	credentialSubject["type"] = credentialType

	return credentialSubject, nil
}

func convertCredentialID(issuerDID *w3c.DID, id string) (string, string, error) {
	issuerID, err := core.IDFromDID(*issuerDID)
	if err != nil {
		return "", "", err
	}
	contractAddress, err := core.EthAddressFromID(issuerID)
	if err != nil {
		return "", "", err
	}
	chainID, err := core.ChainIDfromDID(*issuerDID)
	if err != nil {
		return "", "", err
	}

	contractAddressHex := common.BytesToAddress(contractAddress[:]).Hex()
	id = fmt.Sprintf("urn:iden3:onchain:%d:%s:%s", chainID, contractAddressHex, id)
	return id, contractAddressHex, nil
}

func credentialStatus(issuerDID *w3c.DID, coreClaim *core.Claim) (*verifiable.CredentialStatus, error) {
	revNonce := coreClaim.GetRevocationNonce()
	issuerID, err := core.IDFromDID(*issuerDID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract ID from DID: %w", err)
	}
	contractAddress, err := core.EthAddressFromID(issuerID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract contract address from ID: %w", err)
	}
	chainID, err := core.ChainIDfromDID(*issuerDID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract chainID from DID: %w", err)
	}
	id := fmt.Sprintf("%s/credentialStatus?revocationNonce=%d&contractAddress=%d:%s",
		issuerDID.String(), revNonce, chainID, common.BytesToAddress(contractAddress[:]).Hex())

	return &verifiable.CredentialStatus{
		ID:              id,
		Type:            verifiable.Iden3OnchainSparseMerkleTreeProof2023,
		RevocationNonce: revNonce,
	}, nil
}

func unpackHexToSturcts(credentialHex string) (
	out0 *NonMerklizedIssuerLibCredentialData,
	out1 [8]*big.Int,
	out2 []NonMerklizedIssuerLibSubjectField,
	err error,
) {
	credentialHex = strings.TrimPrefix(credentialHex, "0x")
	credentialHex = strings.TrimPrefix(credentialHex, "0X")
	hexBytes, err := hex.DecodeString(credentialHex)
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to decode hex '%s': %w", credentialHex, err)
	}
	onchainABI, err := OnchainIssuerMetaData.GetAbi()
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to get ABI: %w", err)
	}
	out, err := onchainABI.Unpack("getCredential", hexBytes)
	if err != nil {
		return out0, out1, out2, fmt.Errorf("failed to unpack hex '%s': %w", credentialHex, err)
	}

	out0 = abi.ConvertType(out[0], new(NonMerklizedIssuerLibCredentialData)).(*NonMerklizedIssuerLibCredentialData)
	out1 = *abi.ConvertType(out[1], new([8]*big.Int)).(*[8]*big.Int)
	out2 = *abi.ConvertType(out[2], new([]NonMerklizedIssuerLibSubjectField)).(*[]NonMerklizedIssuerLibSubjectField)
	return out0, out1, out2, nil
}

func extractMTPProof(
	ctx context.Context,
	onchainIssuerClient *OnchainIssuer,
	coreClaim *core.Claim,
	issuerDID *w3c.DID,
) (*verifiable.Iden3SparseMerkleTreeProof, error) {
	// TODO(illia-korotia): remove hindex, hvalue from smart contract
	hindex, err := coreClaim.HIndex()
	if err != nil {
		return nil, err
	}

	mtpProof, err := onchainIssuerClient.GetClaimProof(&bind.CallOpts{Context: ctx}, hindex)
	if err != nil {
		return nil, err
	}
	if !mtpProof.Existence {
		return nil, errors.New("the claim index not exists")
	}

	// TODO (illia-korotia): move to comman function
	latestState, err := onchainIssuerClient.GetLatestPublishedState(nil)
	if err != nil {
		return nil, err
	}
	latestClaimsOfRoot, err := onchainIssuerClient.GetLatestPublishedClaimsRoot(nil)
	if err != nil {
		return nil, err
	}
	latestRevocationOfRoot, err := onchainIssuerClient.GetLatestPublishedRevocationsRoot(nil)
	if err != nil {
		return nil, err
	}
	latestRootsOfRoot, err := onchainIssuerClient.GetLatestPublishedRootsRoot(nil)
	if err != nil {
		return nil, err
	}

	latestStateHash, err := merkletree.NewHashFromBigInt(latestState)
	if err != nil {
		return nil, err
	}
	latestClaimsOfRootHash, err := merkletree.NewHashFromBigInt(latestClaimsOfRoot)
	if err != nil {
		return nil, err
	}
	latestRevocationOfRootHash, err := merkletree.NewHashFromBigInt(latestRevocationOfRoot)
	if err != nil {
		return nil, err
	}
	latestRootsOfRootHash, err := merkletree.NewHashFromBigInt(latestRootsOfRoot)
	if err != nil {
		return nil, err
	}

	coreClaimHex, err := coreClaim.Hex()
	if err != nil {
		return nil, err
	}

	issuerData := verifiable.IssuerData{
		ID: issuerDID.String(),
		State: verifiable.State{
			Value:              strPtr(latestStateHash.Hex()),
			ClaimsTreeRoot:     strPtr(latestClaimsOfRootHash.Hex()),
			RevocationTreeRoot: strPtr(latestRevocationOfRootHash.Hex()),
			RootOfRoots:        strPtr(latestRootsOfRootHash.Hex()),
		},
	}

	p, err := convertChainProofToMerkleProof(mtpProof)
	if err != nil {
		return nil, err
	}

	return &verifiable.Iden3SparseMerkleTreeProof{
		Type:       verifiable.Iden3SparseMerkleTreeProofType,
		CoreClaim:  coreClaimHex,
		IssuerData: issuerData,
		MTP:        p,
	}, nil
}

func convertChainProofToMerkleProof(proof SmtLibProof) (*merkletree.Proof, error) {
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
				return nil, err
			}
			nodeAux.Value, err = merkletree.NewHashFromBigInt(proof.AuxValue)
			if err != nil {
				return nil, err
			}
		}
	}

	allSiblings := make([]*merkletree.Hash, len(proof.Siblings))
	for i, s := range proof.Siblings {
		var sh *merkletree.Hash
		sh, err = merkletree.NewHashFromBigInt(s)
		if err != nil {
			return nil, err
		}
		allSiblings[i] = sh
	}

	p, err := merkletree.NewProofFromData(existence, allSiblings, nodeAux)
	if err != nil {
		return nil, err
	}

	return p, nil
}

func strPtr(s string) *string {
	return &s
}

func validateSourceValue(datatype string, originHash *big.Int, source any) error {
	sourceHash, err := merklize.HashValue(datatype, source)
	if err != nil {
		return fmt.Errorf("failed hash value '%s' with data type '%s': %w", source, datatype, err)
	}
	if sourceHash.Cmp(originHash) != 0 {
		return fmt.Errorf("hash of value '%s' does not match core claim value '%s'", sourceHash, originHash)
	}
	return nil
}
