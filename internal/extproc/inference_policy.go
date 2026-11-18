package extproc

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	stdjson "encoding/json" //nolint:depguard // policy digests use the control plane's canonical stdlib encoding.
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	inferencePolicyBundleHeader  = "x-aiplatform-inference-policy-bundle"
	inferencePolicyAPIVersion    = "policy.maas/v1"
	maxInferencePolicyBytes      = 16 * 1024
	maxInferenceBlockBytes       = 4 * 1024
	maxInferenceInstructionBytes = 12 * 1024
	maxInferencePolicyBlocks     = 32
)

var inferencePolicyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,126}[a-z0-9]$|^[a-z]$`)

type inferencePolicyBundle struct {
	APIVersion string                 `json:"apiVersion"`
	ID         string                 `json:"id"`
	Revision   int64                  `json:"revision"`
	Digest     string                 `json:"digest,omitempty"`
	Modules    inferencePolicyModules `json:"modules"`
}

type inferencePolicyModules struct {
	Conversation *conversationPolicy `json:"conversation,omitempty"`
}

type conversationPolicy struct {
	ClientSystemPrompt string             `json:"clientSystemPrompt"`
	Blocks             []instructionBlock `json:"blocks"`
}

type instructionBlock struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Authority string `json:"authority"`
	Content   string `json:"content"`
}

// inferencePolicyTransport says where an operation's request carries the
// conversation-level system prompt.
type inferencePolicyTransport int

const (
	// policyTransportByField infers the transport from the body's fields and
	// rejects bodies without one. It is kept for operations that have no
	// prompt slot (completions, embeddings) so a bound policy fails closed.
	policyTransportByField inferencePolicyTransport = iota
	// policyTransportChat merges into OpenAI chat messages.
	policyTransportChat
	// policyTransportChatBatch merges into every conversation of vLLM's
	// /v1/chat/completions/batch messages list.
	policyTransportChatBatch
	// policyTransportResponses merges into the Responses API instructions.
	policyTransportResponses
	// policyTransportAnthropic merges into the Anthropic top-level system.
	policyTransportAnthropic
	// policyTransportNone forwards the body unchanged: scoring, ranking and
	// tokenizer operations have no prompt the policy could govern.
	policyTransportNone
)

func normalizeInferencePolicy(header string, body []byte) ([]byte, bool, error) {
	return normalizeInferencePolicyFor(policyTransportByField, header, body)
}

func normalizeInferencePolicyFor(transport inferencePolicyTransport, header string, body []byte) ([]byte, bool, error) {
	if header == "" || transport == policyTransportNone {
		return body, false, nil
	}
	policy, err := decodeInferencePolicy(header)
	if err != nil {
		return nil, false, err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, false, fmt.Errorf("invalid request body for inference policy: %w", err)
	}
	conversation := policy.Modules.Conversation
	platformPrompt := compileConversationPrompt(conversation.Blocks)

	var changed bool
	switch {
	case transport == policyTransportChat:
		changed, err = applyMessagesPolicy(request, platformPrompt, conversation.ClientSystemPrompt)
	case transport == policyTransportChatBatch:
		changed, err = applyBatchMessagesPolicy(request, platformPrompt, conversation.ClientSystemPrompt)
	case transport == policyTransportResponses:
		changed, err = applyStringInstructionPolicy(request, "instructions", platformPrompt, conversation.ClientSystemPrompt)
	case transport == policyTransportAnthropic:
		changed, err = applyAnthropicSystemPolicy(request, platformPrompt, conversation.ClientSystemPrompt)
	case hasField(request, "instructions"):
		changed, err = applyStringInstructionPolicy(request, "instructions", platformPrompt, conversation.ClientSystemPrompt)
	case hasField(request, "system"):
		changed, err = applyAnthropicSystemPolicy(request, platformPrompt, conversation.ClientSystemPrompt)
	case hasField(request, "messages"):
		changed, err = applyMessagesPolicy(request, platformPrompt, conversation.ClientSystemPrompt)
	default:
		return nil, false, errors.New("inference policy requires messages, instructions, or system request field")
	}
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return body, false, nil
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode inference policy request: %w", err)
	}
	return normalized, true, nil
}

func decodeInferencePolicy(header string) (*inferencePolicyBundle, error) {
	if len(header) > maxInferencePolicyBytes {
		return nil, fmt.Errorf("inference policy bundle exceeds %d bytes", maxInferencePolicyBytes)
	}
	decoder := stdjson.NewDecoder(strings.NewReader(header))
	decoder.DisallowUnknownFields()
	var policy inferencePolicyBundle
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("invalid inference policy bundle: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("invalid inference policy bundle: %w", err)
	}
	if err := policy.validate(); err != nil {
		return nil, fmt.Errorf("invalid inference policy bundle: %w", err)
	}
	return &policy, nil
}

func ensureJSONEOF(decoder *stdjson.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (p inferencePolicyBundle) validate() error {
	if p.APIVersion != inferencePolicyAPIVersion {
		return fmt.Errorf("apiVersion must be %q", inferencePolicyAPIVersion)
	}
	if !inferencePolicyIDPattern.MatchString(p.ID) {
		return errors.New("id must be a lowercase policy identifier")
	}
	if p.Revision <= 0 {
		return errors.New("revision must be positive")
	}
	if p.Modules.Conversation == nil {
		return errors.New("modules.conversation is required")
	}
	conversation := p.Modules.Conversation
	switch conversation.ClientSystemPrompt {
	case "append", "discard", "reject":
	default:
		return errors.New("modules.conversation.clientSystemPrompt must be append, discard, or reject")
	}
	if len(conversation.Blocks) == 0 || len(conversation.Blocks) > maxInferencePolicyBlocks {
		return fmt.Errorf("modules.conversation.blocks must contain between 1 and %d entries", maxInferencePolicyBlocks)
	}
	seen := make(map[string]struct{}, len(conversation.Blocks))
	total := 0
	for index, block := range conversation.Blocks {
		if !inferencePolicyIDPattern.MatchString(block.ID) {
			return fmt.Errorf("modules.conversation.blocks[%d].id must be a lowercase policy identifier", index)
		}
		if _, exists := seen[block.ID]; exists {
			return fmt.Errorf("modules.conversation.blocks[%d].id is duplicated", index)
		}
		seen[block.ID] = struct{}{}
		switch block.Kind {
		case "identity", "instruction", "capability_manifest":
		default:
			return fmt.Errorf("modules.conversation.blocks[%d].kind is unsupported", index)
		}
		switch block.Authority {
		case "platform", "tenant", "application":
		default:
			return fmt.Errorf("modules.conversation.blocks[%d].authority is unsupported", index)
		}
		if strings.TrimSpace(block.Content) == "" {
			return fmt.Errorf("modules.conversation.blocks[%d].content is required", index)
		}
		if len([]byte(block.Content)) > maxInferenceBlockBytes {
			return fmt.Errorf("modules.conversation.blocks[%d].content exceeds %d bytes", index, maxInferenceBlockBytes)
		}
		total += len([]byte(block.Content))
	}
	if total > maxInferenceInstructionBytes {
		return fmt.Errorf("instruction content exceeds %d bytes", maxInferenceInstructionBytes)
	}
	if p.Digest == "" {
		return errors.New("digest is required")
	}
	want, err := p.contentDigest()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(p.Digest), []byte(want)) != 1 {
		return errors.New("digest does not match policy content")
	}
	return nil
}

func (p inferencePolicyBundle) contentDigest() (string, error) {
	p.Digest = ""
	encoded, err := stdjson.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode policy digest input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func compileConversationPrompt(blocks []instructionBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Content)
	}
	return strings.Join(parts, "\n\n")
}

func hasField(request map[string]any, name string) bool {
	_, ok := request[name]
	return ok
}

func applyMessagesPolicy(request map[string]any, platformPrompt, mode string) (bool, error) {
	rawMessages, ok := request["messages"].([]any)
	if !ok {
		return false, errors.New("messages must be an array")
	}
	clientHasSystem := false
	filtered := make([]any, 0, len(rawMessages)+1)
	for index, raw := range rawMessages {
		message, ok := raw.(map[string]any)
		if !ok {
			return false, fmt.Errorf("messages[%d] must be an object", index)
		}
		role, _ := message["role"].(string)
		isSystem := role == "system" || role == "developer"
		if isSystem {
			clientHasSystem = true
			if mode == "discard" {
				continue
			}
		}
		filtered = append(filtered, raw)
	}
	if mode == "reject" && clientHasSystem {
		return false, errors.New("client system or developer messages are forbidden by inference policy")
	}
	platformMessage := map[string]any{"role": "system", "content": platformPrompt}
	request["messages"] = append([]any{platformMessage}, filtered...)
	return true, nil
}

// applyBatchMessagesPolicy applies the chat policy to every conversation of a
// batch request independently.
func applyBatchMessagesPolicy(request map[string]any, platformPrompt, mode string) (bool, error) {
	conversations, ok := request["messages"].([]any)
	if !ok {
		return false, errors.New("messages must be an array of conversations")
	}
	for index, raw := range conversations {
		conversation, ok := raw.([]any)
		if !ok {
			return false, fmt.Errorf("messages[%d] must be an array of messages", index)
		}
		single := map[string]any{"messages": conversation}
		if _, err := applyMessagesPolicy(single, platformPrompt, mode); err != nil {
			return false, fmt.Errorf("messages[%d]: %w", index, err)
		}
		conversations[index] = single["messages"]
	}
	request["messages"] = conversations
	return true, nil
}

func applyStringInstructionPolicy(request map[string]any, field, platformPrompt, mode string) (bool, error) {
	raw := request[field]
	if raw == nil {
		request[field] = platformPrompt
		return true, nil
	}
	clientPrompt, ok := raw.(string)
	if !ok {
		return false, fmt.Errorf("%s must be a string", field)
	}
	if mode == "reject" && clientPrompt != "" {
		return false, fmt.Errorf("client %s is forbidden by inference policy", field)
	}
	if mode == "discard" || clientPrompt == "" {
		request[field] = platformPrompt
		return true, nil
	}
	request[field] = platformPrompt + "\n\n" + clientPrompt
	return true, nil
}

func applyAnthropicSystemPolicy(request map[string]any, platformPrompt, mode string) (bool, error) {
	raw := request["system"]
	if raw == nil {
		request["system"] = platformPrompt
		return true, nil
	}
	if mode == "reject" {
		return false, errors.New("client system prompt is forbidden by inference policy")
	}
	if mode == "discard" {
		request["system"] = platformPrompt
		return true, nil
	}
	switch clientPrompt := raw.(type) {
	case string:
		if clientPrompt == "" {
			request["system"] = platformPrompt
		} else {
			request["system"] = platformPrompt + "\n\n" + clientPrompt
		}
	case []any:
		platformBlock := map[string]any{"type": "text", "text": platformPrompt}
		request["system"] = append([]any{platformBlock}, clientPrompt...)
	default:
		return false, errors.New("system must be a string or content block array")
	}
	return true, nil
}
