// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package idcodec encodes a serving backend's identity into the opaque resource ids
// (file ids, batch ids) that the gateway hands back to clients, and decodes them again
// on subsequent requests so the request can be routed back to the same backend.
//
// The id is encrypted and tamper-resistant: clients cannot read the backend identity from
// it, nor forge an id that targets an arbitrary backend. A tampered or otherwise invalid id
// fails to decode and yields [ErrInvalidID].
package idcodec

import "errors"

// Resource kinds encoded into (and validated against) a gateway id. The kind is reflected
// both in the client-visible prefix (e.g. "file-…") and inside the encrypted payload.
const (
	// KindFile identifies a Files API object id ("file-…").
	KindFile = "file"
	// KindBatch identifies a Batch API object id ("batch-…").
	KindBatch = "batch"
	// KindListCursor identifies an opaque Files API list pagination cursor ("flcur-…").
	// It is not an OpenAI object id; it is minted by the gateway as the list endpoint's
	// pagination token (returned as last_id and accepted back via the after query param),
	// carrying the cross-backend walk position inside the encrypted payload.
	KindListCursor = "list_cursor"
)

// ErrInvalidID is returned by [Codec.Decode] when a gateway id cannot be decoded: an
// unknown prefix, a non-decodable body, a decryption/authentication failure, a malformed
// payload, or a kind that does not match the id's prefix.
var ErrInvalidID = errors.New("invalid gateway id")

// BackendID is the decoded content of a gateway-issued id: the owning backend
// (Namespace/Name), the resource Kind, and the backend-native id (NativeID) that must be
// sent upstream in place of the gateway id.
type BackendID struct {
	// Namespace is the Kubernetes namespace of the owning AIServiceBackend.
	Namespace string
	// Name is the name of the owning AIServiceBackend.
	Name string
	// Kind is one of KindFile or KindBatch.
	Kind string
	// NativeID is the backend-native resource id (what the upstream provider issued).
	NativeID string
}

// Codec encodes a [BackendID] into an opaque, client-visible gateway id and decodes it back.
type Codec interface {
	// Encode returns the client-visible gateway id for the given BackendID.
	Encode(BackendID) (string, error)
	// Decode parses a client-visible gateway id back into a BackendID, returning
	// ErrInvalidID if the id is unknown, tampered, or malformed.
	Decode(string) (BackendID, error)
}

// Crypto is the minimal encryption contract the codec relies on. It is intentionally
// structurally compatible with mcpproxy.SessionCrypto so the same audited AES-GCM/PBKDF2
// implementation (and its primary+fallback rotation wrapper) can be reused without an
// import-level coupling.
type Crypto interface {
	// Encrypt encrypts plaintext and returns an opaque (base64) ciphertext string.
	Encrypt(plaintext string) (string, error)
	// Decrypt reverses Encrypt, returning the original plaintext.
	Decrypt(encrypted string) (string, error)
}
