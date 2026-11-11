// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package idcodec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Client-visible id prefixes, mirroring OpenAI's id shapes so existing tooling keeps working.
const (
	filePrefix  = "file-"
	batchPrefix = "batch-"
)

// payloadSeparator separates the kind, "namespace/name", model, and native id fields in the
// pre-encryption payload. Kubernetes names do not contain "|", and the model is validated not to.
const payloadSeparator = "|"

// kdfSalt domain-separates the id keys from any other key derived from the same seed.
const kdfSalt = "aigw-file-id-v1"

// key is one derived key set: an AES-256-GCM AEAD and an HMAC key for the synthetic nonce.
type key struct {
	aead   cipher.AEAD
	macKey []byte
}

// codec implements [Codec] with deterministic authenticated encryption (SIV-style AES-GCM):
// the GCM nonce is a keyed HMAC of the payload rather than random bytes. The same BackendID
// therefore always yields the same id, while different payloads never share a nonce, so GCM
// stays safe. Ids are confidential and tamper-resistant.
type codec struct {
	primary  *key
	fallback *key
}

// NewCodec creates a [Codec] whose keys are derived once from seed with PBKDF2-SHA256. If
// fallbackSeed is non-empty, ids that fail to decode under the primary key are tried with the
// fallback key, allowing the seed to be rotated without invalidating previously issued ids.
func NewCodec(seed string, iterations int, fallbackSeed string, fallbackIterations int) (Codec, error) {
	primary, err := deriveKey(seed, iterations)
	if err != nil {
		return nil, err
	}
	c := &codec{primary: primary}
	if fallbackSeed != "" {
		if c.fallback, err = deriveKey(fallbackSeed, fallbackIterations); err != nil {
			return nil, fmt.Errorf("fallback: %w", err)
		}
	}
	return c, nil
}

// deriveKey derives the AEAD and MAC keys from seed.
func deriveKey(seed string, iterations int) (*key, error) {
	if seed == "" {
		return nil, errors.New("idcodec: seed must not be empty")
	}
	keys, err := pbkdf2.Key(sha256.New, seed, []byte(kdfSalt), iterations, 64)
	if err != nil {
		return nil, fmt.Errorf("idcodec: derive keys: %w", err)
	}
	block, err := aes.NewCipher(keys[:32])
	if err != nil {
		return nil, fmt.Errorf("idcodec: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("idcodec: create gcm: %w", err)
	}
	return &key{aead: aead, macKey: keys[32:]}, nil
}

// nonce returns the deterministic GCM nonce for plaintext.
func (k *key) nonce(plaintext []byte) []byte {
	m := hmac.New(sha256.New, k.macKey)
	m.Write(plaintext)
	return m.Sum(nil)[:k.aead.NonceSize()]
}

// seal returns nonce || ciphertext.
func (k *key) seal(plaintext []byte) []byte {
	n := k.nonce(plaintext)
	return k.aead.Seal(n, n, plaintext, nil)
}

// open authenticates and decrypts nonce || ciphertext, and verifies the nonce is the synthetic
// nonce of the recovered plaintext.
func (k *key) open(raw []byte) ([]byte, bool) {
	ns := k.aead.NonceSize()
	if len(raw) < ns+k.aead.Overhead() {
		return nil, false
	}
	p, err := k.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil || !hmac.Equal(raw[:ns], k.nonce(p)) {
		return nil, false
	}
	return p, true
}

// prefixForKind returns the client-visible id prefix for a resource kind.
func prefixForKind(kind string) (string, error) {
	switch kind {
	case KindFile:
		return filePrefix, nil
	case KindBatch:
		return batchPrefix, nil
	default:
		return "", fmt.Errorf("idcodec: unsupported kind %q", kind)
	}
}

// Encode implements [Codec.Encode].
func (c *codec) Encode(b *BackendID) (string, error) {
	prefix, err := prefixForKind(b.Kind)
	if err != nil {
		return "", err
	}
	if b.Namespace == "" || b.Name == "" || b.NativeID == "" {
		return "", errors.New("idcodec: namespace, name, and nativeID are required")
	}
	if strings.Contains(b.Model, payloadSeparator) {
		return "", fmt.Errorf("idcodec: model must not contain %q", payloadSeparator)
	}
	// payload: kind|namespace/name|model|nativeID. Decode uses SplitN so a native id that itself
	// contains the separator is preserved.
	payload := strings.Join([]string{b.Kind, b.Namespace + "/" + b.Name, b.Model, b.NativeID}, payloadSeparator)
	// URL base64 without padding keeps the id within the OpenAI id charset [A-Za-z0-9_-].
	return prefix + base64.RawURLEncoding.EncodeToString(c.primary.seal([]byte(payload))), nil
}

// Decode implements [Codec.Decode].
func (c *codec) Decode(id string) (BackendID, error) {
	var kind, body string
	switch {
	case strings.HasPrefix(id, filePrefix):
		kind, body = KindFile, id[len(filePrefix):]
	case strings.HasPrefix(id, batchPrefix):
		kind, body = KindBatch, id[len(batchPrefix):]
	default:
		return BackendID{}, ErrInvalidID
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return BackendID{}, ErrInvalidID
	}
	p, ok := c.primary.open(raw)
	if !ok && c.fallback != nil {
		p, ok = c.fallback.open(raw)
	}
	if !ok {
		return BackendID{}, ErrInvalidID
	}

	parts := strings.SplitN(string(p), payloadSeparator, 4)
	// The kind in the encrypted payload must match the prefix the client presented, so a file id
	// cannot be replayed as a batch id (or vice versa).
	if len(parts) != 4 || parts[0] != kind {
		return BackendID{}, ErrInvalidID
	}
	nsName := strings.SplitN(parts[1], "/", 2)
	if len(nsName) != 2 || nsName[0] == "" || nsName[1] == "" || parts[3] == "" {
		return BackendID{}, ErrInvalidID
	}
	return BackendID{
		Namespace: nsName[0],
		Name:      nsName[1],
		Kind:      kind,
		Model:     parts[2],
		NativeID:  parts[3],
	}, nil
}
