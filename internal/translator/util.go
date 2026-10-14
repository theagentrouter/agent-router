// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	mimeTypeImageJPEG       = "image/jpeg"
	mimeTypeImagePNG        = "image/png"
	mimeTypeImageGIF        = "image/gif"
	mimeTypeImageWEBP       = "image/webp"
	mimeTypeTextPlain       = "text/plain"
	mimeTypeApplicationJSON = "application/json"
	mimeTypeApplicationEnum = "text/x.enum"
)

var (
	// sseDataPrefixSpace is the canonical form the gateway emits; reads go through
	// cutSSEDataPrefix, which also accepts the field without the space.
	sseDataPrefixSpace = []byte("data: ")
	sseDataPrefix      = []byte("data:")
	sseDoneMessage     = []byte("[DONE]")
	sseDoneFullLine    = append(append(sseDataPrefixSpace, sseDoneMessage...), '\n')
)

// cutSSEFieldPrefix strips an SSE field-name prefix such as "data:" from a line,
// accepting the optional space after the colon:
// https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation
func cutSSEFieldPrefix(line, fieldName []byte) ([]byte, bool) {
	after, ok := bytes.CutPrefix(line, fieldName)
	if !ok {
		return nil, false
	}
	// Only one leading space belongs to the framing; anything beyond it is data.
	return bytes.TrimPrefix(after, []byte{' '}), true
}

// cutSSEDataPrefix is cutSSEFieldPrefix for the "data" field.
func cutSSEDataPrefix(line []byte) ([]byte, bool) {
	return cutSSEFieldPrefix(line, sseDataPrefix)
}

// nextSSEEvent returns the next complete SSE event and preserves partial events in buffer.
// SSE permits either LF or CRLF line endings.
func nextSSEEvent(buffer []byte) (event, remaining []byte, ok bool) {
	lfEnd := bytes.Index(buffer, []byte("\n\n"))
	crlfEnd := bytes.Index(buffer, []byte("\r\n\r\n"))

	switch {
	case crlfEnd >= 0 && (lfEnd < 0 || crlfEnd < lfEnd):
		return buffer[:crlfEnd], buffer[crlfEnd+len("\r\n\r\n"):], true
	case lfEnd >= 0:
		return buffer[:lfEnd], buffer[lfEnd+len("\n\n"):], true
	default:
		return nil, buffer, false
	}
}

// regDataURI follows the web uri regex definition.
// https://developer.mozilla.org/en-US/docs/Web/URI/Schemes/data#syntax
var regDataURI = regexp.MustCompile(`\Adata:(.+?)?(;base64)?,`)

// parseDataURI parse data uri example: data:image/jpeg;base64,/9j/4AAQSkZJRgABAgAAZABkAAD.
func parseDataURI(uri string) (string, []byte, error) {
	matches := regDataURI.FindStringSubmatch(uri)
	if len(matches) != 3 {
		return "", nil, fmt.Errorf("data uri does not have a valid format")
	}
	l := len(matches[0])
	contentType := matches[1]
	bin, err := base64.StdEncoding.DecodeString(uri[l:])
	if err != nil {
		return "", nil, err
	}
	return contentType, bin, nil
}

// forceOriginalBodyIfEmpty returns original when forceBodyMutation is set and newBody
// hasn't been populated by the caller, so that a body is always written to Envoy
// when body mutation is forced. Otherwise, newBody is returned unchanged.
func forceOriginalBodyIfEmpty(forceBodyMutation bool, newBody, original []byte) []byte {
	if forceBodyMutation && len(newBody) == 0 {
		return original
	}
	return newBody
}

// systemMsgToDeveloperMsg converts OpenAI system message to developer message.
// Since systemMsg is deprecated, this function is provided to maintain backward compatibility.
func systemMsgToDeveloperMsg(msg openai.ChatCompletionSystemMessageParam) openai.ChatCompletionDeveloperMessageParam {
	// Convert OpenAI system message to developer message.
	return openai.ChatCompletionDeveloperMessageParam{
		Name:    msg.Name,
		Role:    openai.ChatMessageRoleDeveloper,
		Content: msg.Content,
	}
}

// serialize a ChatCompletionResponseChunk, this is common for all chat completion request
func serializeOpenAIChatCompletionChunk(chunk *openai.ChatCompletionResponseChunk, buf *[]byte) error {
	var chunkBytes []byte
	chunkBytes, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("failed to marshal stream chunk: %w", err)
	}
	*buf = append(*buf, sseDataPrefixSpace...)
	*buf = append(*buf, chunkBytes...)
	*buf = append(*buf, '\n', '\n')
	return nil
}
