// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testupstreamlib

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/tidwall/gjson"
	"golang.org/x/exp/rand"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Server is a test upstream server that responds with the expected content and headers.
// It also checks if the request content matches the expected headers, path, and body.
type Server struct {
	// ID is the identifier of the test upstream server that can be used to verify routing.
	ID string
	// Logger is the logger used to log messages.
	Logger *log.Logger

	streamingInterval time.Duration
}

// DoMain starts the server and listens on the given listener.
func (s *Server) DoMain(ct context.Context, l net.Listener) {
	s.streamingInterval = 200 * time.Millisecond
	server := &http.Server{} //nolint
	go func() {
		<-ct.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			s.Logger.Printf("failed to shutdown the server: %v", err)
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", s.handler)
	server.Handler = mux
	s.Logger.Printf("starting test upstream server %q on %s\n", s.ID, l.Addr().String())
	if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.Logger.Fatalf("failed to serve: %v", err)
	}
	s.Logger.Println("server shutdown gracefully")
}

// s.logAndSendError logs the error and sends a proper error response with details
func (s *Server) logAndSendError(w http.ResponseWriter, code int, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	s.Logger.Printf("ERROR [%d]: %s", code, msg)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-TestUpstream-Error", "true")
	w.WriteHeader(code)
	fmt.Fprintf(w, format+"\n", a...) //nolint:errcheck
}

func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	for k, v := range r.Header {
		s.Logger.Printf("header %q: %s\n", k, v)
	}
	if v := r.Header.Get(ExpectedHostKey); v != "" {
		if r.Host != v {
			s.logAndSendError(w, http.StatusBadRequest, "unexpected host: got %q, expected %q", r.Host, v)
			return
		}
		s.Logger.Println("host matched:", v)
	} else {
		s.Logger.Println("no expected host: got", r.Host)
	}
	if v := r.Header.Get(ExpectedHeadersKey); v != "" {
		expectedHeaders, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the expected headers: %v", err)
			return
		}
		s.Logger.Println("expected headers", string(expectedHeaders))

		// Comma separated key-value pairs.
		for kv := range bytes.SplitSeq(expectedHeaders, []byte(",")) {
			parts := bytes.SplitN(kv, []byte(":"), 2)
			if len(parts) != 2 {
				s.logAndSendError(w, http.StatusBadRequest, "invalid header key-value pair: %s", string(kv))
				return
			}
			key := string(parts[0])
			value := string(parts[1])
			if r.Header.Get(key) != value {
				s.logAndSendError(w, http.StatusBadRequest, "unexpected header %q: got %q, expected %q", key, r.Header.Get(key), value)
				return
			}
			s.Logger.Printf("header %q matched %s\n", key, value)
		}
	} else {
		s.Logger.Println("no expected headers")
	}

	if v := r.Header.Get(NonExpectedRequestHeadersKey); v != "" {
		nonExpectedHeaders, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the non-expected headers: %v", err)
			return
		}
		s.Logger.Println("non-expected headers", string(nonExpectedHeaders))

		// Comma separated key-value pairs.
		for kv := range bytes.SplitSeq(nonExpectedHeaders, []byte(",")) {
			key := string(kv)
			if r.Header.Get(key) != "" {
				s.logAndSendError(w, http.StatusBadRequest, "unexpected header %q presence with value %q", key, r.Header.Get(key))
				return
			}
			s.Logger.Printf("header %q absent\n", key)
		}
	} else {
		s.Logger.Println("no non-expected headers in the request")
	}

	if v := r.Header.Get(ExpectedTestUpstreamIDKey); v != "" {
		if s.ID != v {
			msg := fmt.Sprintf("unexpected testupstream-id: received by '%s' but expected '%s'\n", s.ID, v)
			s.logAndSendError(w, http.StatusBadRequest, "%s", msg)
			return
		}
		s.Logger.Println("testupstream-id matched:", v)
	} else {
		s.Logger.Println("no expected testupstream-id")
	}

	if expectedPath := r.Header.Get(ExpectedPathHeaderKey); expectedPath != "" {
		expectedPath, err := base64.StdEncoding.DecodeString(expectedPath)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the expected path: %v", err)
			return
		}

		if r.URL.Path != string(expectedPath) {
			s.logAndSendError(w, http.StatusBadRequest, "unexpected path: got %s, expected %s", r.URL.Path, string(expectedPath))
			return
		}
	}

	if expectedRawQuery := r.Header.Get(ExpectedRawQueryHeaderKey); expectedRawQuery != "" {
		if r.URL.RawQuery != expectedRawQuery {
			s.logAndSendError(w, http.StatusBadRequest, "unexpected raw query: got %s, expected %s", r.URL.RawQuery, expectedRawQuery)
			return
		}
	}

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		s.logAndSendError(w, http.StatusInternalServerError, "failed to read the request body: %v", err)
		return
	}
	s.Logger.Printf("Request body (%d bytes)", len(requestBody))

	// At least for the endpoints we want to support, all requests should have a Content-Length header
	// and should not use chunked transfer encoding.
	if r.Header.Get("Content-Length") == "" {
		// Endpoint pickers mutate the request body by sending them back to the client (due to the use of DUPLEX mode),
		// and it will clear the Content-Length header. It should be fine to assume that these locally hosted endpoints
		// are capable of reading the chunked transfer encoding unlike the GCP Anthropic.
		if r.Header.Get(internalapi.EndpointPickerHeaderKey) == "" {
			s.logAndSendError(w, http.StatusBadRequest, "no Content-Length header, using request body length: %d", len(requestBody))
			return
		}
	}

	if expectedReqBody := r.Header.Get(ExpectedRequestBodyHeaderKey); expectedReqBody != "" {
		var expectedBody []byte
		expectedBody, err = base64.StdEncoding.DecodeString(expectedReqBody)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the expected request body: %v", err)
			return
		}

		if !bytes.Equal(expectedBody, requestBody) {
			s.logAndSendError(w, http.StatusBadRequest, "unexpected request body: got %s, expected %s", string(requestBody), string(expectedBody))
			return
		}
	} else {
		s.Logger.Println("no expected request body")
	}

	if v := r.Header.Get(ResponseHeadersKey); v != "" {
		var responseHeaders []byte
		responseHeaders, err = base64.StdEncoding.DecodeString(v)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the response headers: %v", err)
			return
		}
		s.Logger.Println("response headers", string(responseHeaders))

		// Comma separated key-value pairs.
		for kv := range bytes.SplitSeq(responseHeaders, []byte(",")) {
			parts := bytes.SplitN(kv, []byte(":"), 2)
			if len(parts) != 2 {
				s.logAndSendError(w, http.StatusBadRequest, "invalid header key-value pair: %s", string(kv))
				return
			}
			key := string(parts[0])
			value := string(parts[1])
			w.Header().Set(key, value)
			s.Logger.Printf("response header %q set to %s\n", key, value)
		}
	} else {
		s.Logger.Println("no response headers")
	}
	w.Header().Set("testupstream-id", s.ID)
	status := http.StatusOK
	if v := r.Header.Get(ResponseStatusKey); v != "" {
		status, err = strconv.Atoi(v)
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to parse the response status: %v", err)
			return
		}
	}

	// Do the best-effort model detection for logging and verification.
	model := gjson.GetBytes(requestBody, "model")
	if model.Exists() {
		s.Logger.Println("detected model in the request:", model)
		// Set the model in the response header for verification.
		w.Header().Set("X-Model", model.String())
	}

	switch r.Header.Get(ResponseTypeKey) {
	case "sse", "sse-gzip":
		isSSEGzip := r.Header.Get(ResponseTypeKey) == "sse-gzip"
		w.Header().Set("Content-Type", "text/event-stream")
		if isSSEGzip {
			w.Header().Set("Content-Encoding", "gzip")
		}

		var expResponseBody []byte
		expResponseBody, err = base64.StdEncoding.DecodeString(r.Header.Get(ResponseBodyHeaderKey))
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the response body: %v", err)
			return
		}

		w.WriteHeader(status)

		// writer is either the raw http.ResponseWriter or a gzip.Writer wrapping it.
		var writer io.Writer = w
		var gz *gzip.Writer
		if isSSEGzip {
			gz = gzip.NewWriter(w)
			writer = gz
		}
		flush := func() {
			if gz != nil {
				// Flush the gzip writer to emit a complete compressed block,
				// ensuring the downstream sees distinct gzip frames per chunk.
				_ = gz.Flush()
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			} else {
				panic("expected http.ResponseWriter to be an http.Flusher")
			}
		}

		// Auto-detect the SSE format. If the body contains the event message separator "\n\n",
		// we treat it as a stream of pre-formatted "raw" SSE events. Otherwise, we treat it
		// as a simple line-by-line stream that needs to be formatted.
		if bytes.Contains(expResponseBody, []byte("\n\n")) {
			eventBlocks := bytes.SplitSeq(expResponseBody, []byte("\n\n"))

			for block := range eventBlocks {
				// Skip any empty blocks that can result from splitting.
				if len(bytes.TrimSpace(block)) == 0 {
					continue
				}
				time.Sleep(s.streamingInterval)

				// Write the complete event block followed by the required double newline delimiter.
				if _, err = writer.Write(append(block, "\n\n"...)); err != nil {
					s.Logger.Println("failed to write the response body")
					return
				}

				flush()
				s.Logger.Println("response block sent:", string(block))
			}

		} else {
			s.Logger.Println("detected line-by-line stream, formatting as SSE")
			lines := bytes.SplitSeq(expResponseBody, []byte("\n"))

			for line := range lines {
				if len(line) == 0 {
					continue
				}
				time.Sleep(s.streamingInterval)

				// Format the line as an SSE 'data' message.
				if _, err = fmt.Fprintf(writer, "data: %s\n\n", line); err != nil {
					s.Logger.Println("failed to write the response body")
					return
				}

				flush()
				s.Logger.Println("response line sent:", string(line))
			}
		}

		if gz != nil {
			_ = gz.Close()
		}
		s.Logger.Println("response sent")
		r.Context().Done()
	case "aws-event-stream":
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")

		var expResponseBody []byte
		expResponseBody, err = base64.StdEncoding.DecodeString(r.Header.Get(ResponseBodyHeaderKey))
		if err != nil {
			s.logAndSendError(w, http.StatusBadRequest, "failed to decode the response body: %v", err)
			return
		}

		w.WriteHeader(status)
		e := eventstream.NewEncoder()
		for line := range bytes.SplitSeq(expResponseBody, []byte("\n")) {
			// Write each line as a chunk with AWS Event Stream format.
			if len(line) == 0 {
				continue
			}
			time.Sleep(s.streamingInterval)
			var bedrockStreamEvent map[string]any
			err = json.Unmarshal(line, &bedrockStreamEvent)
			if err != nil {
				s.Logger.Println("failed to decode the response body")
			}
			var eventType string
			if _, ok := bedrockStreamEvent["role"]; ok {
				eventType = "messageStart"
			} else if _, ok = bedrockStreamEvent["start"]; ok {
				eventType = "contentBlockStart"
			} else if _, ok = bedrockStreamEvent["delta"]; ok {
				eventType = "contentBlockDelta"
			} else if _, ok = bedrockStreamEvent["stopReason"]; ok {
				eventType = "messageStop"
			} else if _, ok = bedrockStreamEvent["usage"]; ok {
				eventType = "metadata"
			} else if _, ok = bedrockStreamEvent["contentBlockIndex"]; ok {
				eventType = "contentBlockStop"
			}
			if err = e.Encode(w, eventstream.Message{
				Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue(eventType)}},
				Payload: line,
			}); err != nil {
				s.Logger.Println("failed to encode the response body")
			}
			w.(http.Flusher).Flush()
			s.Logger.Println("response line sent:", string(line))
		}

		if err = e.Encode(w, eventstream.Message{
			Headers: eventstream.Headers{{Name: "event-type", Value: eventstream.StringValue("end")}},
			Payload: []byte("this-is-end"),
		}); err != nil {
			s.Logger.Println("failed to encode the response body")
		}

		s.Logger.Println("response sent")
		r.Context().Done()
	default:
		isGzip := r.Header.Get(ResponseTypeKey) == "gzip"
		if isGzip {
			w.Header().Set("content-encoding", "gzip")
		}
		// The content-type header might have been already set via ResponseHeadersKey.
		// If not, set it to application/json by default.
		if w.Header().Get("content-type") == "" {
			w.Header().Set("content-type", "application/json")
		}
		var responseBody []byte
		if expResponseBody := r.Header.Get(ResponseBodyHeaderKey); expResponseBody == "" {
			// If the expected response body is not set, get the fake response if the path is known.
			responseBody, err = getFakeResponse(r.URL.Path, r.Header)
			if err != nil {
				s.logAndSendError(w, http.StatusBadRequest, "failed to get the fake response for path %s: %v", r.URL.Path, err)
				return
			}
		} else {
			responseBody, err = base64.StdEncoding.DecodeString(expResponseBody)
			if err != nil {
				s.logAndSendError(w, http.StatusBadRequest, "failed to decode the response body: %v", err)
				return
			}
		}

		w.WriteHeader(status)
		if isGzip {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			_, _ = gz.Write(responseBody)
			_ = gz.Close()
			responseBody = buf.Bytes()
		}
		_, _ = w.Write(responseBody)
	}
}

var chatCompletionFakeResponses = []string{
	`This is a test.`,
	`The quick brown fox jumps over the lazy dog.`,
	`Lorem ipsum dolor sit amet, consectetur adipiscing elit.`,
	`To be or not to be, that is the question.`,
	`All your base are belong to us.`,
	`I am the bone of my sword.`,
	`I am the master of my fate.`,
	`I am the captain of my soul.`,
	`I am the master of my fate, I am the captain of my soul.`,
	`I am the bone of my sword, steel is my body, and fire is my blood.`,
	`The quick brown fox jumps over the lazy dog.`,
	`Lorem ipsum dolor sit amet, consectetur adipiscing elit.`,
	`To be or not to be, that is the question.`,
	`All your base are belong to us.`,
	`Omae wa mou shindeiru.`,
	`Nani?`,
	`I am inevitable.`,
	`May the Force be with you.`,
	`Houston, we have a problem.`,
	`I'll be back.`,
	`You can't handle the truth!`,
	`Here's looking at you, kid.`,
	`Go ahead, make my day.`,
	`I see dead people.`,
	`Hasta la vista, baby.`,
	`You're gonna need a bigger boat.`,
	`E.T. phone home.`,
	`I feel the need - the need for speed.`,
	`I'm king of the world!`,
	`Show me the money!`,
	`You had me at hello.`,
	`I'm the king of the world!`,
	`To infinity and beyond!`,
	`You're a wizard, Harry.`,
	`I solemnly swear that I am up to no good.`,
	`Mischief managed.`,
	`Expecto Patronum!`,
}

func getFakeResponse(path string, headers http.Header) ([]byte, error) {
	switch path {
	case "/non-llm-route":
		const template = `{"message":"This is a non-LLM endpoint response"}`
		return []byte(template), nil
	case "/v1/chat/completions":
		if v := headers.Get(FakeResponseHeaderKey); v != "" {
			fake, ok := chatCompletionsFakeResponses[v]
			if !ok {
				return nil, fmt.Errorf("unknown large fake response key: %s", headers.Get(FakeResponseHeaderKey))
			}
			return fake, nil
		}
		const template = `{"choices": [{"index": 0,"message": {"role": "assistant","content": "%s"},"finish_reason": "stop"}],"usage": {"prompt_tokens": 1,"completion_tokens": 100,"total_tokens": 300}}`
		msg := fmt.Sprintf(template,
			//nolint:gosec
			chatCompletionFakeResponses[rand.New(rand.NewSource(uint64(time.Now().UnixNano()))).
				Intn(len(chatCompletionFakeResponses))])
		return []byte(msg), nil
	case "/model/gpt-4/converse": // Only used in benchmark tests.
		if v := headers.Get(FakeResponseHeaderKey); v != "" {
			fake, ok := awsBedrockBenchmarkResponses[v]
			if ok {
				return fake, nil
			}
		}
		return nil, fmt.Errorf("unknown large fake response key: %s", headers.Get(FakeResponseHeaderKey))
	case "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gpt-4:generateContent": // Only used in benchmark tests.
		if v := headers.Get(FakeResponseHeaderKey); v != "" {
			fake, ok := gcpVertexAIBenchmarkResponses[v]
			if ok {
				return fake, nil
			}
		}
		return nil, fmt.Errorf("unknown large fake response key: %s", headers.Get(FakeResponseHeaderKey))
	case "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/gpt-4:rawPredict": // Only used in benchmark tests.
		if v := headers.Get(FakeResponseHeaderKey); v != "" {
			fake, ok := gcpAnthropicAIBenchmarkResponses[v]
			if ok {
				return fake, nil
			}
		}
		return nil, fmt.Errorf("unknown large fake response key: %s", headers.Get(FakeResponseHeaderKey))
	case "/v1/embeddings":
		const embeddingTemplate = `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3,0.4,0.5],"index":0}],"model":"some-cool-self-hosted-model","usage":{"prompt_tokens":3,"total_tokens":3}}`
		return []byte(embeddingTemplate), nil
	default:
		return nil, fmt.Errorf("unknown path: %s", path)
	}
}

var (
	chatCompletionsFakeResponses = map[string][]byte{
		"small":  newChatCompletionsLargeFakeResponse(100),    // ~5KB
		"medium": newChatCompletionsLargeFakeResponse(10000),  // ~500KB
		"large":  newChatCompletionsLargeFakeResponse(100000), // ~5MB
	}
	awsBedrockBenchmarkResponses = map[string][]byte{
		"small":  newAWSBedrockBenchmarkResponse(100),    // ~1KB
		"medium": newAWSBedrockBenchmarkResponse(10000),  // ~500KB
		"large":  newAWSBedrockBenchmarkResponse(100000), // ~5MB
	}
	gcpVertexAIBenchmarkResponses = map[string][]byte{
		"small":  newGCPVertexAIBenchmarkResponse(100),    // ~1KB
		"medium": newGCPVertexAIBenchmarkResponse(10000),  // ~500KB
		"large":  newGCPVertexAIBenchmarkResponse(100000), // ~5MB
	}
	gcpAnthropicAIBenchmarkResponses = map[string][]byte{
		"small":  newGCPAnthropicAIBenchmarkResponse(100),    // ~1KB
		"medium": newGCPAnthropicAIBenchmarkResponse(10000),  // ~500KB
		"large":  newGCPAnthropicAIBenchmarkResponse(100000), // ~5MB
	}
)

func newChatCompletionsLargeFakeResponse(count int) []byte {
	const template = `{
  "id": "chatcmpl-CqSf2jfV03XXLdrL2CoreXibglhVU",
  "object": "chat.completion",
  "created": 1766619264,
  "model": "gpt-4o-mini-2024-07-18",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "%s",
        "refusal": null,
        "annotations": []
      },
      "logprobs": null,
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 13,
    "completion_tokens": 12,
    "total_tokens": 25,
    "prompt_tokens_details": {
      "cached_tokens": 0,
      "audio_tokens": 0
    },
    "completion_tokens_details": {
      "reasoning_tokens": 0,
      "audio_tokens": 0,
      "accepted_prediction_tokens": 0,
      "rejected_prediction_tokens": 0
    }
  },
  "service_tier": "default",
  "system_fingerprint": "fp_8bbc38b4db"
}`
	// Generate a large content string of approximately 3MB.
	var largeContent bytes.Buffer
	for range count {
		largeContent.WriteString("This is a line in the large fake response content. ")
	}
	return []byte(fmt.Sprintf(template, largeContent.String()))
}

func newAWSBedrockBenchmarkResponse(numBytes int) []byte {
	const template = `{"output": {"message": {"content": [%s], "role": "assistant"}}, "stopReason": "end_turn", "usage": {"inputTokens": 1, "outputTokens": 2, "totalTokens": 3}}`
	var contentParts []string
	for range 10 {
		contentParts = append(contentParts, fmt.Sprintf(`{"text": "%s"}`, strings.Repeat("A", numBytes)))
	}
	return []byte(fmt.Sprintf(template, strings.Join(contentParts, ", ")))
}

func newGCPVertexAIBenchmarkResponse(numBytes int) []byte {
	const template = `{"candidates": [{"content": {"parts": [%s], "role": "model"}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 2, "totalTokenCount": 3}}`
	var contentParts []string
	for range 10 {
		contentParts = append(contentParts, fmt.Sprintf(`{"type": "text", "text": "%s"}`, strings.Repeat("B", numBytes)))
	}
	return []byte(fmt.Sprintf(template, strings.Join(contentParts, ", ")))
}

func newGCPAnthropicAIBenchmarkResponse(numBytes int) []byte {
	const template = `{"id": "msg_benchmark", "type": "message", "role": "assistant", "stop_reason": "end_turn", "content": [%s], "usage": {"input_tokens": 1, "output_tokens": 2}}`
	var contentParts []string
	for range 10 {
		contentParts = append(contentParts, fmt.Sprintf(`{"type": "text", "text": "%s"}`, strings.Repeat("C", numBytes)))
	}
	return []byte(fmt.Sprintf(template, strings.Join(contentParts, ", ")))
}
