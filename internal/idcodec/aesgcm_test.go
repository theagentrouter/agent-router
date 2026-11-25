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
)

// idCharset matches the OpenAI id charset that gateway ids must stay within.
var idCharset = regexp.MustCompile(`^(file|batch)-[A-Za-z0-9_-]+$`)

// newCodec returns a real codec with a low PBKDF2 iteration count for fast tests.
func newCodec(seed, fallbackSeed string) Codec {
	c, err := NewCodec(seed, 4096, fallbackSeed, 4096)
	if err != nil {
		panic(err)
	}
	return c
}

func TestCodec_RoundTrip(t *testing.T) {
	c := newCodec("primary-seed", "")

	for _, tc := range []BackendID{
		{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"},
		{Namespace: "default", Name: "openai", Kind: KindBatch, NativeID: "batch_xyz789"},
		{Namespace: "ns-2", Name: "b_3", Kind: KindFile, NativeID: "file-with|separator"},
	} {
		t.Run(tc.Kind+"/"+tc.NativeID, func(t *testing.T) {
			id, err := c.Encode(&tc)
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

func TestCodec_EncodeErrors(t *testing.T) {
	c := newCodec("primary-seed", "")

	_, err := c.Encode(&BackendID{Namespace: "ns", Name: "b", Kind: "bogus", NativeID: "i"})
	require.Error(t, err)

	for _, missing := range []BackendID{
		{Name: "b", Kind: KindFile, NativeID: "i"},
		{Namespace: "ns", Kind: KindFile, NativeID: "i"},
		{Namespace: "ns", Name: "b", Kind: KindFile},
	} {
		_, err = c.Encode(&missing)
		require.Error(t, err)
	}
}

func TestCodec_Tamper(t *testing.T) {
	c := newCodec("primary-seed", "")
	id, err := c.Encode(&BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// Flip one character in the middle of the body; it stays a valid base64url char but
	// changes the ciphertext, so GCM authentication fails on decode.
	idx := len(filePrefix) + (len(id)-len(filePrefix))/2
	tampered := id[:idx] + flipChar(id[idx]) + id[idx+1:]
	require.NotEqual(t, id, tampered)

	_, err = c.Decode(tampered)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestCodec_Rotation(t *testing.T) {
	id, err := newCodec("old-seed", "").Encode(
		&BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// New primary with the old key as fallback still decodes ids issued under the old key.
	out, err := newCodec("new-seed", "old-seed").Decode(id)
	require.NoError(t, err)
	require.Equal(t, "file-abc123", out.NativeID)

	// New primary without the old fallback can no longer decode it.
	_, err = newCodec("new-seed", "").Decode(id)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestCodec_KindPrefixCrossCheck(t *testing.T) {
	c := newCodec("primary-seed", "")
	id, err := c.Encode(&BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, NativeID: "file-abc123"})
	require.NoError(t, err)

	// Swap the prefix to claim a different kind; the payload kind no longer matches.
	swapped := batchPrefix + strings.TrimPrefix(id, filePrefix)
	_, err = c.Decode(swapped)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestCodec_Model(t *testing.T) {
	c := newCodec("primary-seed", "")

	in := BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, Model: "gpt-4o-mini", NativeID: "file-abc|x"}
	id, err := c.Encode(&in)
	require.NoError(t, err)
	out, err := c.Decode(id)
	require.NoError(t, err)
	require.Equal(t, in, out)

	// A model containing the payload separator is rejected rather than corrupting the payload.
	_, err = c.Encode(&BackendID{Namespace: "ns1", Name: "apple", Kind: KindFile, Model: "a|b", NativeID: "file-abc"})
	require.Error(t, err)
}

func TestCodec_InvalidInputs(t *testing.T) {
	c := newCodec("primary-seed", "")

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

func TestCodec_StableIDs(t *testing.T) {
	c := newCodec("seed", "")
	in := &BackendID{Namespace: "ns", Name: "apple", Kind: KindFile, Model: "m", NativeID: "file-1"}
	a, err := c.Encode(in)
	require.NoError(t, err)
	b, err := c.Encode(in)
	require.NoError(t, err)
	require.Equal(t, a, b)

	other, err := c.Encode(&BackendID{Namespace: "ns", Name: "apple", Kind: KindFile, Model: "m", NativeID: "file-2"})
	require.NoError(t, err)
	require.NotEqual(t, a, other)

	// A different seed cannot decode.
	_, err = newCodec("other-seed", "").Decode(a)
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestNewCodec_EmptySeed(t *testing.T) {
	_, err := NewCodec("", 1, "", 0)
	require.Error(t, err)
	_, err = NewCodec("seed", 1, "", 0)
	require.NoError(t, err)
}
