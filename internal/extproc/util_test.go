// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
)

func TestIsGoodStatusCode(t *testing.T) {
	for _, s := range []int{200, 201, 299} {
		require.True(t, isGoodStatusCode(s))
	}
	for _, s := range []int{100, 300, 400, 500} {
		require.False(t, isGoodStatusCode(s))
	}
}

func TestDecodeContentIfNeeded(t *testing.T) {
	tests := []struct {
		name         string
		body         []byte
		encoding     string
		wantEncoded  bool
		wantEncoding string
		wantErr      bool
	}{
		{
			name:         "plain body",
			body:         []byte("hello world"),
			encoding:     "",
			wantEncoded:  false,
			wantEncoding: "",
			wantErr:      false,
		},
		{
			name:         "unsupported encoding",
			body:         []byte("hello world"),
			encoding:     "deflate",
			wantEncoded:  false,
			wantEncoding: "",
			wantErr:      false,
		},
		{
			name: "valid gzip",
			body: func() []byte {
				var b bytes.Buffer
				w := gzip.NewWriter(&b)
				_, err := w.Write([]byte("abc"))
				if err != nil {
					panic(err)
				}
				w.Close()
				return b.Bytes()
			}(),
			encoding:     "gzip",
			wantEncoded:  true,
			wantEncoding: "gzip",
			wantErr:      false,
		},
		{
			name:         "invalid gzip",
			body:         []byte("not a gzip"),
			encoding:     "gzip",
			wantEncoded:  false,
			wantEncoding: "",
			wantErr:      true,
		},
		{
			// An empty body with Content-Encoding: gzip must not error. gzip.NewReader
			// over zero bytes returns io.EOF; treating that as a decode failure aborts
			// an otherwise-successful upstream response.
			name:         "empty gzip body",
			body:         []byte{},
			encoding:     "gzip",
			wantEncoded:  false,
			wantEncoding: "",
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := decodeContentIfNeeded(tt.body, tt.encoding)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantEncoded, res.isEncoded)
			if !tt.wantEncoded {
				out, _ := io.ReadAll(res.reader)
				require.Equal(t, tt.body, out)
			} else if tt.encoding == "gzip" && !tt.wantErr {
				out, _ := io.ReadAll(res.reader)
				require.Equal(t, []byte("abc"), out)
			}
		})
	}
}

func TestStreamingGzipDecompression(t *testing.T) {
	// Simulate a gzip-compressed SSE stream split into multiple chunks.
	// This tests the same accumulate-and-redecode logic used by decodeStreamingContent.
	messages := []string{
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hello\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}

	// Produce per-chunk gzip output by flushing after each message.
	var allCompressed bytes.Buffer
	gz := gzip.NewWriter(&allCompressed)
	var chunkBoundaries []int // byte offsets in allCompressed marking each chunk end
	for _, msg := range messages {
		_, err := gz.Write([]byte(msg))
		require.NoError(t, err)
		require.NoError(t, gz.Flush())
		chunkBoundaries = append(chunkBoundaries, allCompressed.Len())
	}
	require.NoError(t, gz.Close())
	fullCompressed := allCompressed.Bytes()

	// Split the compressed output into chunks at flush boundaries,
	// plus a final chunk with the gzip footer.
	var chunks [][]byte
	prev := 0
	for _, boundary := range chunkBoundaries {
		chunks = append(chunks, fullCompressed[prev:boundary])
		prev = boundary
	}
	if prev < len(fullCompressed) {
		// Remaining bytes contain the gzip footer.
		chunks = append(chunks, fullCompressed[prev:])
	}

	// Simulate the accumulate-and-redecode algorithm from decodeStreamingContent.
	var compressedBuf []byte
	decompressedOffset := 0
	var totalDecompressed string

	for i, chunk := range chunks {
		endOfStream := i == len(chunks)-1
		compressedBuf = append(compressedBuf, chunk...)

		result, err := decodeContentIfNeeded(compressedBuf, "gzip")
		require.NoError(t, err)
		require.True(t, result.isEncoded)

		allDecompressed, readErr := io.ReadAll(result.reader)
		if readErr != nil {
			if endOfStream {
				t.Fatalf("unexpected error on final chunk: %v", readErr)
			}
			// io.ErrUnexpectedEOF is expected for non-final chunks.
			require.ErrorIs(t, readErr, io.ErrUnexpectedEOF)
		}

		newData := allDecompressed[decompressedOffset:]
		decompressedOffset = len(allDecompressed)
		totalDecompressed += string(newData)
	}

	expected := messages[0] + messages[1] + messages[2]
	require.Equal(t, expected, totalDecompressed)
}

// TestDecodeStreamingContent_TruncatedGzipAtEndOfStream verifies that when the gzip
// stream is cut short before its footer arrives (e.g. the upstream connection resets
// during streaming), decodeStreamingContent returns the partial data that decompressed
// cleanly rather than failing the whole response. Without this tolerance the client
// receives an empty 200 instead of the bytes that did arrive.
func TestDecodeStreamingContent_TruncatedGzipAtEndOfStream(t *testing.T) {
	messages := []string{
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hello\"}}\n\n",
	}

	// Produce a gzip stream flushed after each message but never closed, so the footer
	// (CRC32 + ISIZE) is never written: this mimics an upstream reset mid-stream.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	var chunkBoundaries []int
	for _, msg := range messages {
		_, err := gz.Write([]byte(msg))
		require.NoError(t, err)
		require.NoError(t, gz.Flush())
		chunkBoundaries = append(chunkBoundaries, buf.Len())
	}
	// Note: gz.Close() is intentionally NOT called, so the footer is absent.
	truncated := buf.Bytes()

	var chunks [][]byte
	prev := 0
	for _, boundary := range chunkBoundaries {
		chunks = append(chunks, truncated[prev:boundary])
		prev = boundary
	}

	u := &chatCompletionProcessorUpstreamFilter{responseEncoding: "gzip"}

	var totalDecompressed string
	for _, chunk := range chunks {
		result, err := u.decodeStreamingContent(chunk)
		// Even on the final chunk, a missing footer (ErrUnexpectedEOF) must not be fatal.
		require.NoError(t, err)
		require.True(t, result.isEncoded)

		data, readErr := io.ReadAll(result.reader)
		require.NoError(t, readErr)
		totalDecompressed += string(data)
	}

	// All bytes written before the connection reset are delivered to the client.
	require.Equal(t, messages[0]+messages[1], totalDecompressed)
}

// TestDecodeStreamingContent_CorruptGzipStillFails verifies that genuine corruption
// (as opposed to truncation) remains fatal: a malformed gzip header reports an error
// other than io.ErrUnexpectedEOF and is not silently tolerated.
func TestDecodeStreamingContent_CorruptGzipStillFails(t *testing.T) {
	u := &chatCompletionProcessorUpstreamFilter{responseEncoding: "gzip"}
	_, err := u.decodeStreamingContent([]byte("this is not a gzip stream"))
	require.Error(t, err)
}

func TestRemoveContentEncodingIfNeeded(t *testing.T) {
	tests := []struct {
		name        string
		hm          *extprocv3.HeaderMutation
		bm          *extprocv3.BodyMutation
		isEncoded   bool
		wantRemoved bool
	}{
		{
			name:        "no body mutation, not encoded",
			hm:          nil,
			bm:          nil,
			isEncoded:   false,
			wantRemoved: false,
		},
		{
			name:        "body mutation, not encoded",
			hm:          nil,
			bm:          &extprocv3.BodyMutation{},
			isEncoded:   false,
			wantRemoved: false,
		},
		{
			name:        "body mutation, encoded",
			hm:          nil,
			bm:          &extprocv3.BodyMutation{},
			isEncoded:   true,
			wantRemoved: true,
		},
		{
			name:        "existing header mutation, body mutation, encoded",
			hm:          &extprocv3.HeaderMutation{RemoveHeaders: []string{"foo"}},
			bm:          &extprocv3.BodyMutation{},
			isEncoded:   true,
			wantRemoved: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := removeContentEncodingIfNeeded(tt.hm, tt.bm, tt.isEncoded)
			if tt.wantRemoved {
				require.Contains(t, res.RemoveHeaders, "content-encoding")
			} else if res != nil {
				require.NotContains(t, res.RemoveHeaders, "content-encoding")
			}
		})
	}
}

func TestHeaderMutationCarrier(t *testing.T) {
	t.Run("Get panics", func(t *testing.T) {
		carrier := &headerMutationCarrier{m: &extprocv3.HeaderMutation{}}
		require.Panics(t, func() { carrier.Get("test-key") })
	})

	t.Run("Keys panics", func(t *testing.T) {
		carrier := &headerMutationCarrier{m: &extprocv3.HeaderMutation{}}
		require.Panics(t, func() { carrier.Keys() })
	})

	t.Run("Set headers", func(t *testing.T) {
		mutation := &extprocv3.HeaderMutation{}
		carrier := &headerMutationCarrier{m: mutation}

		carrier.Set("trace-id", "12345")
		carrier.Set("span-id", "67890")

		require.Equal(t, &extprocv3.HeaderMutation{
			SetHeaders: []*corev3.HeaderValueOption{
				{
					Header: &corev3.HeaderValue{
						Key:      "trace-id",
						RawValue: []byte("12345"),
					},
				},
				{
					Header: &corev3.HeaderValue{
						Key:      "span-id",
						RawValue: []byte("67890"),
					},
				},
			},
		}, mutation)
	})
}
