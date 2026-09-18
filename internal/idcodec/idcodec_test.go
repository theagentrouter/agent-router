// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package idcodec

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/mcpproxy"
)

// idCharset matches the OpenAI id charset that gateway ids must stay within.
var idCharset = regexp.MustCompile(`^(file|batch)-[A-Za-z0-9_-]+$`)

// newCrypto returns a real PBKDF2/AES-GCM Crypto so the authenticated-encryption behavior
// (tamper rejection, key rotation) is exercised end to end.
func newCrypto(seed string) Crypto {
	return mcpproxy.NewPBKDF2AesGcmSessionCrypto(seed, 4096)
}

func TestAESGCMCodec_RoundTrip(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)

	for _, tc := range []BackendID{
		{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"},
		{Namespace: "default", Name: "openai", Kind: KindBatch, NativeID: "batch_xyz789"},
		{Namespace: "ns-2", Name: "b_3", Kind: KindFile, NativeID: "file-with|separator"},
	} {
		t.Run(tc.Kind+"/"+tc.NativeID, func(t *testing.T) {
			id, err := c.Encode(tc)
			require.NoError(t, err)
			require.Regexp(t, idCharset, id)

			switch tc.Kind {
			case KindFile:
				require.True(t, strings.HasPrefix(id, filePrefix))
			case KindBatch:
				require.True(t, strings.HasPrefix(id, batchPrefix))
			}

			out, err := c.Decode(id)
			require.NoError(t, err)
			require.Equal(t, tc, out)
		})
	}
}

func TestAESGCMCodec_EncodeErrors(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)

	_, err := c.Encode(BackendID{Namespace: "ns", Name: "b", Kind: "bogus", NativeID: "i"})
	require.Error(t, err)

	for _, missing := range []BackendID{
		{Name: "b", Kind: KindFile, NativeID: "i"},
		{Namespace: "ns", Kind: KindFile, NativeID: "i"},
		{Namespace: "ns", Name: "b", Kind: KindFile},
	} {
		_, err = c.Encode(missing)
		require.Error(t, err)
	}
}

func TestAESGCMCodec_Tamper(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)
	id, err := c.Encode(BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// Flip one character in the middle of the body; it stays a valid base64url char but
	// changes the ciphertext, so GCM authentication fails on decode.
	idx := len(filePrefix) + (len(id)-len(filePrefix))/2
	tampered := id[:idx] + flipChar(id[idx]) + id[idx+1:]
	require.NotEqual(t, id, tampered)

	_, err = c.Decode(tampered)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestAESGCMCodec_Rotation(t *testing.T) {
	oldKey := newCrypto("old-seed")
	newKey := newCrypto("new-seed")

	id, err := NewAESGCMCodec(oldKey, nil).Encode(
		BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// New primary with the old key as fallback still decodes ids issued under the old key.
	out, err := NewAESGCMCodec(newKey, oldKey).Decode(id)
	require.NoError(t, err)
	require.Equal(t, "file-abc123", out.NativeID)

	// New primary without the old fallback can no longer decode it.
	_, err = NewAESGCMCodec(newKey, nil).Decode(id)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestAESGCMCodec_KindPrefixCrossCheck(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)
	id, err := c.Encode(BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// Swap the prefix to claim a different kind; the payload kind no longer matches.
	swapped := batchPrefix + strings.TrimPrefix(id, filePrefix)
	_, err = c.Decode(swapped)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestAESGCMCodec_ListCursor(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)

	// cursorCharset matches the URL-safe shape a list cursor must keep (it travels as a query
	// parameter and a JSON string value).
	cursorCharset := regexp.MustCompile(`^flcur-[A-Za-z0-9_-]+$`)

	for _, tc := range []BackendID{
		// Walk position with a non-empty packed native cursor.
		{Namespace: "ns1", Name: "apple", Kind: KindListCursor, NativeID: "ns1/apple|file-after-99"},
		// "Start of backend" position: the packed payload keeps it non-empty.
		{Namespace: "ns2", Name: "banana", Kind: KindListCursor, NativeID: "ns1/apple|"},
	} {
		t.Run(tc.NativeID, func(t *testing.T) {
			id, err := c.Encode(tc)
			require.NoError(t, err)
			require.Regexp(t, cursorCharset, id)
			require.True(t, strings.HasPrefix(id, listCursorPrefix))

			out, err := c.Decode(id)
			require.NoError(t, err)
			require.Equal(t, tc, out)
		})
	}

	// A list cursor must not decode as a file id (and vice versa): kinds are cross-checked
	// against the prefix, and the prefixes themselves differ.
	cursor, err := c.Encode(BackendID{Namespace: "ns1", Name: "apple", Kind: KindListCursor, NativeID: "ns1/apple|x"})
	require.NoError(t, err)
	swapped := filePrefix + strings.TrimPrefix(cursor, listCursorPrefix)
	_, err = c.Decode(swapped)
	require.ErrorIs(t, err, ErrInvalidID)

	// A list cursor survives key rotation via the fallback.
	rotated, err := NewAESGCMCodec(newCrypto("new-seed"), newCrypto("primary-seed")).Decode(cursor)
	require.NoError(t, err)
	require.Equal(t, KindListCursor, rotated.Kind)
	require.Equal(t, "ns1/apple|x", rotated.NativeID)

	// Tampering with the body fails authentication.
	idx := len(listCursorPrefix) + (len(cursor)-len(listCursorPrefix))/2
	tampered := cursor[:idx] + flipChar(cursor[idx]) + cursor[idx+1:]
	_, err = c.Decode(tampered)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestAESGCMCodec_InvalidInputs(t *testing.T) {
	c := NewAESGCMCodec(newCrypto("primary-seed"), nil)

	for _, id := range []string{
		"",
		"no-prefix",
		"randomstring",
		filePrefix,           // empty body
		filePrefix + "!!!",   // not base64url
		batchPrefix + "@@@@", // not base64url
	} {
		_, err := c.Decode(id)
		require.ErrorIs(t, err, ErrInvalidID, "id=%q", id)
	}
}

// flipChar returns a different base64url character than the input.
func flipChar(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}
