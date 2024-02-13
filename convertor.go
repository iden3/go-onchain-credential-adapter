package convertor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	core "github.com/iden3/go-iden3-core/v2"
	"github.com/iden3/go-iden3-core/v2/w3c"
	contractABI "github.com/iden3/go-onchain-credential-adapter/internal"
	"github.com/iden3/go-schema-processor/v2/merklize"
	"github.com/iden3/go-schema-processor/v2/verifiable"
	"github.com/piprate/json-gold/ld"
)

const (
	// SupportedMajorCredentialProtocolVersion is the major version of the credential protocol supported by this package.
	SupportedMajorCredentialProtocolVersion = 0

	booleanHashTrue  = "18586133768512220936620570745912940619677854269274689475585506675881198879027"
	booleanHashFalse = "19014214495641488759237505126948346942972912379615652741039992445865937985820"
)

var (
	credentialContexts = [2]string{
		verifiable.JSONLDSchemaW3CCredential2018,
		verifiable.JSONLDSchemaIden3Credential,
	}
)

type convertorOptions struct {
	MerklizeOptions merklize.Options
}

type option func(*convertorOptions)

func WithMerklizeOptions(merklizeOptions merklize.Options) option {
	return func(opts *convertorOptions) {
		opts.MerklizeOptions = merklizeOptions
	}
}

// W3CcredentialFromOnchainData returns a W3C credential from onchain data.
func W3CcredentialFromOnchainData(
	ctx context.Context,
	ethcli *ethclient.Client,
	issuerDID *w3c.DID,
	subjectDID *w3c.DID,
	credentialID *big.Int,
	opts ...option,
) (*verifiable.W3CCredential, error) {
	o := &convertorOptions{}
	for _, opt := range opts {
		opt(o)
	}

	adapter, err := newAdapter(ctx, ethcli, issuerDID)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter for onchain contract: %w", err)
	}

	protocolVersion, err := adapter.credentialProtocolVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get protocol version: %w", err)
	}
	if err := isSupportedCredentialProtocolVersion(protocolVersion); err != nil {
		return nil, err
	}

	subjectID, err := core.IDFromDID(*subjectDID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract ID from DID '%s': %w", subjectDID, err)
	}
	credentialData, coreClaim, credentialSubjectFields, err := adapter.fetchOnchainDataForSubject(ctx, subjectID.BigInt(), credentialID)
	if err != nil {
		return nil,
			fmt.Errorf("failed to fetch ochain data by id '%s' for subject '%s': %w", credentialID, subjectDID, err)
	}

	return convertOnchainDataToW3CCredential(
		ctx,
		adapter,
		credentialData,
		coreClaim,
		credentialSubjectFields,
		o,
	)
}

// W3CcredentialFromOnchainHex returns a W3C credential from onchain hex data.
func W3CcredentialFromOnchainHex(
	ctx context.Context,
	ethcli *ethclient.Client,
	issuerDID *w3c.DID,
	hexdata string,
	opts ...option,
) (*verifiable.W3CCredential, error) {
	o := &convertorOptions{}
	for _, opt := range opts {
		opt(o)
	}

	adapter, err := newAdapter(ctx, ethcli, issuerDID)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter for onchain contract: %w", err)
	}
	credentialData, coreClaim, credentialSubjectFields, err := adapter.unpackHexToSturcts(hexdata)
	if err != nil {
		return nil,
			fmt.Errorf("failed to unpack hexdata: %w", err)
	}

	return convertOnchainDataToW3CCredential(
		ctx,
		adapter,
		credentialData,
		coreClaim,
		credentialSubjectFields,
		o,
	)
}

func convertOnchainDataToW3CCredential(
	ctx context.Context,
	adapter *adapter,
	credentialData contractABI.INonMerklizedIssuerCredentialData,
	coreClaimBigInts [8]*big.Int,
	credentialSubjectFields []contractABI.INonMerklizedIssuerSubjectField,
	opts *convertorOptions,
) (*verifiable.W3CCredential, error) {
	coreClaim, err := core.NewClaimFromBigInts(coreClaimBigInts)
	if err != nil {
		return nil,
			fmt.Errorf("failed to create core claim: %w", err)
	}

	var expirationTime *time.Time
	expT, ok := coreClaim.GetExpirationDate()
	if ok {
		e := expT.UTC()
		expirationTime = &e
	}
	issuanceTime := time.Unix(int64(credentialData.IssuanceDate), 0).UTC()

	credentialSubject, err := convertCredentialSubject(
		coreClaim,
		credentialData.Context,
		credentialData.Type,
		credentialSubjectFields,
		&opts.MerklizeOptions,
	)
	if err != nil {
		return nil,
			fmt.Errorf("failed to convert credential subject: %w", err)
	}

	existenceProof, err := adapter.existenceProof(ctx, coreClaim)
	if err != nil {
		return nil, err
	}

	return &verifiable.W3CCredential{
		ID:      adapter.credentialID(credentialData.Id.String()),
		Context: append(credentialContexts[:], credentialData.Context...),
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
		Issuer:            adapter.did.String(),
		CredentialStatus:  adapter.credentialStatus(coreClaim.GetRevocationNonce()),
		CredentialSubject: credentialSubject,
		Proof:             verifiable.CredentialProofs{existenceProof},
	}, nil
}

func convertCredentialSubject(
	coreClaim *core.Claim,
	contractContexts []string,
	credentialType string,
	credentialSubjectFields []contractABI.INonMerklizedIssuerSubjectField,
	marklizeInstance *merklize.Options,
) (map[string]any, error) {
	contextbytes, err := json.Marshal(map[string][]string{
		"@context": append(credentialContexts[:], contractContexts...),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal context: %w", err)
	}

	credentialSubject := make(map[string]any)
	for _, f := range credentialSubjectFields {
		datatype, err := marklizeInstance.TypeFromContext(
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
				return nil, fmt.Errorf("unsupported boolean value '%s'", f.Value)
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
				return nil, fmt.Errorf("field '%s': %w", f.Key, err)
			}
			credentialSubject[f.Key] = source
		case ld.XSDNS + "dateTime":
			timestamp := big.NewInt(0).SetBytes(f.RawValue)
			sourceTime := time.Unix(
				timestamp.Int64(),
				0,
			).UTC().Format(time.RFC3339Nano)
			if err := validateSourceValue(datatype, f.Value, sourceTime); err != nil {
				return nil, fmt.Errorf("filed '%s': %w", f.Key, err)
			}
			credentialSubject[f.Key] = sourceTime
		case ld.XSDDouble:
			v, _, err := big.NewFloat(0).Parse(
				hex.EncodeToString(f.RawValue), 16)
			if err != nil {
				return nil,
					fmt.Errorf("failed to convert string '%s' to float for field '%s'", f.RawValue, f.Key)
			}
			sourceDouble, _ := v.Float64()
			if err := validateSourceValue(datatype, f.Value, sourceDouble); err != nil {
				return nil, fmt.Errorf("field '%s': %w", f.Key, err)
			}
			credentialSubject[f.Key] = sourceDouble
		default:
			return nil, fmt.Errorf("unsupported type for key '%s': %s", f.Key, datatype)
		}
	}
	credentialSubject["type"] = credentialType

	subjectID, err := coreClaim.GetID()
	if errors.Is(err, core.ErrNoID) { // self claim
		return credentialSubject, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to get ID from core claim: %w", err)
	}

	subjectDID, err := core.ParseDIDFromID(subjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert subjectID to DID: %w", err)
	}
	credentialSubject["id"] = subjectDID.String()

	return credentialSubject, nil
}

func validateSourceValue(datatype string, originHash *big.Int, source any) error {
	sourceHash, err := merklize.HashValue(datatype, source)
	if err != nil {
		return fmt.Errorf("failed hash value '%s' with data type '%s': %w", source, datatype, err)
	}
	if sourceHash.Cmp(originHash) != 0 {
		return fmt.Errorf("source value '%s' does not match origin value '%s'", sourceHash, originHash)
	}
	return nil
}

func isSupportedCredentialProtocolVersion(version string) error {
	versionParts := strings.Split(version, ".")
	if len(versionParts) != 3 {
		return fmt.Errorf("invalid version format '%s'", version)
	}

	major, err := strconv.Atoi(versionParts[0])
	if err != nil {
		return fmt.Errorf("failed to parse major version '%s': %w", version, err)
	}

	if major != SupportedMajorCredentialProtocolVersion {
		return fmt.Errorf("unsupported major version '%d'. supported major version '%d'",
			major, SupportedMajorCredentialProtocolVersion)
	}

	return nil
}
