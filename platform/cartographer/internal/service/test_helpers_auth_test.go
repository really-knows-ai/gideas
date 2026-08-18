package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/metadata"
)

func signCapabilities(caps string, priv ed25519.PrivateKey) (sig string, unixTimestamp int64) {
	unixTimestamp = time.Now().Unix()
	payload := fmt.Sprintf("%s|%d", caps, unixTimestamp)
	rawSig := ed25519.Sign(priv, []byte(payload))
	sig = base64.StdEncoding.EncodeToString(rawSig)
	return
}

// capabilityContext returns a context with signed capability metadata.
// The signedBy parameter must be "operator" or "sidecar".
func capabilityContext(caps string, priv ed25519.PrivateKey, signedBy string) context.Context {
	return capabilityContextAt(caps, priv, signedBy, time.Now().Unix())
}

func capabilityContextAt(caps string, priv ed25519.PrivateKey, signedBy string, signedAt int64) context.Context {
	payload := fmt.Sprintf("%s|%d", caps, signedAt)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, caps,
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		flowmeta.MetadataKeyCapabilitiesSignedBy, signedBy,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}
