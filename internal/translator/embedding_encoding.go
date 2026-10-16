// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"encoding/base64"
	"encoding/binary"
	"math"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

func encodeEmbedding(values []float64, encodingFormat *string) openai.EmbeddingUnion {
	if encodingFormat == nil || *encodingFormat != "base64" {
		return openai.EmbeddingUnion{Value: values}
	}

	// OpenAI clients decode base64 embeddings as little-endian float32 values.
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(float32(value)))
	}
	return openai.EmbeddingUnion{Value: base64.StdEncoding.EncodeToString(data)}
}
