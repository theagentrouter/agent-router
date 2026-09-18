// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package idcodec

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Client-visible id prefixes, mirroring OpenAI's id shapes so existing tooling keeps working.
const (
	filePrefix  = "file-"
	batchPrefix = "batch-"
	// listCursorPrefix marks an opaque Files API list pagination cursor. It is not an OpenAI
	// object id, so it does not need to match an OpenAI id shape; it only needs to be URL-safe
	// (it travels as the after query parameter and the last_id response field).
	listCursorPrefix = "flcur-"
)

// payloadSeparator separates the kind, "namespace/name", and native id fields in the
// pre-encryption payload. Kubernetes names and OpenAI native ids do not contain "|".
const payloadSeparator = "|"

// aesGCMCodec implements [Codec] by encrypting the payload with a primary [Crypto] and
// decrypting with the primary first, then an optional fallback (to support key rotation).
type aesGCMCodec struct {
	primary  Crypto
	fallback Crypto
}

// NewAESGCMCodec creates a [Codec] backed by the given primary [Crypto]. If fallback is
// non-nil it is tried on decode when the primary fails, allowing the primary key to be
// rotated while ids issued under the previous key remain decodable.
func NewAESGCMCodec(primary, fallback Crypto) Codec {
	return &aesGCMCodec{primary: primary, fallback: fallback}
}

// prefixForKind returns the client-visible id prefix for a resource kind.
func prefixForKind(kind string) (string, error) {
	switch kind {
	case KindFile:
		return filePrefix, nil
	case KindBatch:
		return batchPrefix, nil
	case KindListCursor:
		return listCursorPrefix, nil
	default:
		return "", fmt.Errorf("idcodec: unsupported kind %q", kind)
	}
}

// Encode implements [Codec.Encode].
func (c *aesGCMCodec) Encode(b BackendID) (string, error) {
	prefix, err := prefixForKind(b.Kind)
	if err != nil {
		return "", err
	}
	if b.Namespace == "" || b.Name == "" || b.NativeID == "" {
		return "", fmt.Errorf("idcodec: namespace, name, and nativeID are required")
	}

	// payload: kind|namespace/name|nativeID. Decode uses SplitN so a native id that itself
	// contains the separator is preserved.
	payload := strings.Join([]string{b.Kind, b.Namespace + "/" + b.Name, b.NativeID}, payloadSeparator)

	enc, err := c.primary.Encrypt(payload)
	if err != nil {
		return "", fmt.Errorf("idcodec: encrypt: %w", err)
	}

	// Crypto returns standard base64; re-encode the raw ciphertext as URL base64 without
	// padding so the final id stays within the OpenAI id charset [A-Za-z0-9_-].
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("idcodec: transcode: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode implements [Codec.Decode].
func (c *aesGCMCodec) Decode(id string) (BackendID, error) {
	var kind, body string
	switch {
	case strings.HasPrefix(id, filePrefix):
		kind, body = KindFile, id[len(filePrefix):]
	case strings.HasPrefix(id, batchPrefix):
		kind, body = KindBatch, id[len(batchPrefix):]
	case strings.HasPrefix(id, listCursorPrefix):
		kind, body = KindListCursor, id[len(listCursorPrefix):]
	default:
		return BackendID{}, ErrInvalidID
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return BackendID{}, ErrInvalidID
	}
	enc := base64.StdEncoding.EncodeToString(raw)

	payload, err := c.decrypt(enc)
	if err != nil {
		return BackendID{}, ErrInvalidID
	}

	parts := strings.SplitN(payload, payloadSeparator, 3)
	if len(parts) != 3 {
		return BackendID{}, ErrInvalidID
	}
	// Cross-check: the kind embedded in the encrypted payload must match the prefix the
	// client presented, so a file id cannot be replayed as a batch id (or vice versa).
	if parts[0] != kind {
		return BackendID{}, ErrInvalidID
	}
	nsName := strings.SplitN(parts[1], "/", 2)
	if len(nsName) != 2 || nsName[0] == "" || nsName[1] == "" || parts[2] == "" {
		return BackendID{}, ErrInvalidID
	}

	return BackendID{
		Namespace: nsName[0],
		Name:      nsName[1],
		Kind:      kind,
		NativeID:  parts[2],
	}, nil
}

// decrypt tries the primary Crypto first and falls back to the secondary (if configured).
func (c *aesGCMCodec) decrypt(enc string) (string, error) {
	plaintext, err := c.primary.Decrypt(enc)
	if err == nil {
		return plaintext, nil
	}
	if c.fallback != nil {
		return c.fallback.Decrypt(enc)
	}
	return "", err
}
