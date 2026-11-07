// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
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

// sseEvents returns n server-sent events of varying length.
func sseEvents(n int) []string {
	events := make([]string, n)
	for i := range events {
		events[i] = fmt.Sprintf("data: {\"index\":%d,\"delta\":%q}\n\n", i, strings.Repeat("token ", i%7+1))
	}
	return events
}

// compressEvents compresses events the way a streaming upstream does, flushing after each one, so
// chunk i of the result decompresses to events[i]. The last chunk is what closing the stream wrote.
func compressEvents(t *testing.T, contentEncoding string, events []string) [][]byte {
	var buf bytes.Buffer
	var w interface {
		io.WriteCloser
		Flush() error
	}
	switch contentEncoding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "br":
		w = brotli.NewWriter(&buf)
	default:
		t.Fatalf("unsupported content encoding %q", contentEncoding)
	}
	chunks := make([][]byte, 0, len(events)+1)
	for _, e := range events {
		_, err := w.Write([]byte(e))
		require.NoError(t, err)
		require.NoError(t, w.Flush())
		chunks = append(chunks, bytes.Clone(buf.Bytes()))
		buf.Reset()
	}
	require.NoError(t, w.Close())
	return append(chunks, bytes.Clone(buf.Bytes()))
}

func TestStreamDecoder(t *testing.T) {
	events := sseEvents(200)
	plain := strings.Join(events, "")
	for _, encoding := range []string{"gzip", "br"} {
		chunks := compressEvents(t, encoding, events)
		compressed := bytes.Join(chunks, nil)

		t.Run(encoding+"/each flushed event decodes from its own chunk", func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			d := newStreamDecoder(encoding)
			for i, e := range events {
				out, err := d.decode(chunks[i], false)
				require.NoError(t, err)
				require.Equal(t, e, string(out), "chunk %d", i)
			}
			out, err := d.decode(chunks[len(events)], true)
			require.NoError(t, err)
			require.Empty(t, out)
		})

		for _, size := range []int{1, 3, 100, len(compressed)} {
			t.Run(fmt.Sprintf("%s/chunks of %d bytes", encoding, size), func(t *testing.T) {
				defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
				d := newStreamDecoder(encoding)
				var got []byte
				for chunk := range slices.Chunk(compressed, size) {
					out, err := d.decode(chunk, false)
					require.NoError(t, err)
					got = append(got, out...)
				}
				out, err := d.decode(nil, true)
				require.NoError(t, err)
				require.Equal(t, plain, string(append(got, out...)))
			})
		}

		t.Run(encoding+"/truncated at end of stream", func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			d := newStreamDecoder(encoding)
			out, err := d.decode(bytes.Join(chunks[:len(events)], nil), false)
			require.NoError(t, err)
			require.Equal(t, plain, string(out))
			_, err = d.decode(nil, true)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		})

		t.Run(encoding+"/data after the compressed stream", func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			d := newStreamDecoder(encoding)
			_, err := d.decode(append(bytes.Clone(compressed), "trailing"...), true)
			require.Error(t, err)
		})

		t.Run(encoding+"/stop releases a stream that ended early", func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			d := newStreamDecoder(encoding)
			out, err := d.decode(chunks[0], false)
			require.NoError(t, err)
			require.Equal(t, events[0], string(out))
			d.stop()
			require.ErrorIs(t, d.err, errStreamDecoderStopped)
		})
	}

	t.Run("unsupported encoding passes chunks through", func(t *testing.T) {
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
		d := newStreamDecoder("deflate")
		for _, chunk := range []string{"first", "second"} {
			out, err := d.decode([]byte(chunk), false)
			require.NoError(t, err)
			require.Equal(t, chunk, string(out))
		}
		out, err := d.decode(nil, true)
		require.NoError(t, err)
		require.Empty(t, out)
	})
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
