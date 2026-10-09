// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// MessagesRequest represents a request to the Anthropic Messages API.
// https://docs.claude.com/en/api/messages
//
// Note that we currently only have "passthrough-ish" translators for Anthropic,
// so this struct only contains fields that are necessary for minimal processing
// as well as for observability purposes on a best-effort basis.
//
// Notably, round trip idempotency is not guaranteed when using this struct.
type MessagesRequest struct {
	// Model is the model to use for the request.
	Model string `json:"model"`

	// Messages is the list of messages in the conversation.
	// https://docs.claude.com/en/api/messages#body-messages
	Messages []MessageParam `json:"messages"`

	// MaxTokens is the maximum number of tokens to generate.
	// https://docs.claude.com/en/api/messages#body-max-tokens
	MaxTokens float64 `json:"max_tokens"`

	// Container identifier for reuse across requests.
	// https://docs.claude.com/en/api/messages#body-container
	Container *Container `json:"container,omitempty"`

	// ContextManagement is the context management configuration.
	// https://docs.claude.com/en/api/messages#body-context-management
	ContextManagement *ContextManagement `json:"context_management,omitempty"`

	// MCPServers is the list of MCP servers.
	// https://docs.claude.com/en/api/messages#body-mcp-servers
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`

	// Metadata is the metadata for the request.
	// https://docs.claude.com/en/api/messages#body-metadata
	Metadata *MessagesMetadata `json:"metadata,omitempty"`

	// ServiceTier indicates the service tier for the request.
	// https://docs.claude.com/en/api/messages#body-service-tier
	ServiceTier *MessageServiceTier `json:"service_tier,omitempty"`

	// StopSequences is the list of stop sequences.
	// https://docs.claude.com/en/api/messages#body-stop-sequences
	StopSequences []string `json:"stop_sequences,omitempty"`

	// System is the system prompt to guide the model's behavior.
	// https://docs.claude.com/en/api/messages#body-system
	System *SystemPrompt `json:"system,omitempty"`

	// Temperature controls the randomness of the output.
	Temperature *float64 `json:"temperature,omitempty"`

	// Thinking is the configuration for the model's "thinking" behavior.
	// https://docs.claude.com/en/api/messages#body-thinking
	Thinking *Thinking `json:"thinking,omitempty"`

	// ToolChoice indicates the tool choice for the model.
	// https://docs.claude.com/en/api/messages#body-tool-choice
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`

	// Tools is the list of tools available to the model.
	// https://docs.claude.com/en/api/messages#body-tools
	Tools []ToolUnion `json:"tools,omitempty"`

	// Stream indicates whether to stream the response.
	Stream bool `json:"stream,omitempty"`

	// TopP is the cumulative probability for nucleus sampling.
	TopP *float64 `json:"top_p,omitempty"`

	// TopK is the number of highest probability vocabulary tokens to keep for top-k-filtering.
	TopK *int `json:"top_k,omitempty"`
}

// MessageParam represents a single message in the Anthropic Messages API.
// https://platform.claude.com/docs/en/api/messages#message_param
type MessageParam struct {
	// Role is the role of the message. "user" or "assistant".
	Role MessageRole `json:"role"`

	// Content is the content of the message.
	Content MessageContent `json:"content"`
}

// MessageRole represents the role of a message in the Anthropic Messages API.
// https://docs.claude.com/en/api/messages#body-messages-role
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// MessageContent represents the content of a message in the Anthropic Messages API.
// https://docs.claude.com/en/api/messages#body-messages-content
type MessageContent struct {
	Text  string              // Non-empty if this is not array content.
	Array []ContentBlockParam // Non-empty if this is array content.
}

func (m *MessageContent) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first.
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		m.Text = text
		return nil
	}

	// Try to unmarshal as array of MessageContentArrayElement.
	var array []ContentBlockParam
	if err := json.Unmarshal(data, &array); err == nil {
		m.Array = array
		return nil
	}
	return fmt.Errorf("message content must be either text or array")
}

func (m *MessageContent) MarshalJSON() ([]byte, error) {
	if m.Text != "" {
		return json.Marshal(m.Text)
	}
	if len(m.Array) > 0 {
		return json.Marshal(m.Array)
	}
	return nil, fmt.Errorf("message content must have either text or array")
}

type (
	// ContentBlockParam represents an element of the array content in a message.
	// https://platform.claude.com/docs/en/api/messages#body-messages-content
	ContentBlockParam struct {
		Text                *TextBlockParam
		Image               *ImageBlockParam
		Document            *DocumentBlockParam
		SearchResult        *SearchResultBlockParam
		Thinking            *ThinkingBlockParam
		RedactedThinking    *RedactedThinkingBlockParam
		ToolUse             *ToolUseBlockParam
		ToolResult          *ToolResultBlockParam
		ServerToolUse       *ServerToolUseBlockParam
		WebSearchToolResult *WebSearchToolResultBlockParam
	}

	// TextBlockParam represents a text content block.
	// https://platform.claude.com/docs/en/api/messages#text_block_param
	TextBlockParam struct {
		Text         string         `json:"text"`
		Type         string         `json:"type"` // Always "text".
		CacheControl *CacheControl  `json:"cache_control,omitempty"`
		Citations    []TextCitation `json:"citations,omitempty"`
	}

	// ImageBlockParam represents an image content block.
	// https://platform.claude.com/docs/en/api/messages#image_block_param
	ImageBlockParam struct {
		Type         string        `json:"type"` // Always "image".
		Source       ImageSource   `json:"source"`
		CacheControl *CacheControl `json:"cache_control,omitempty"`
	}

	// DocumentBlockParam represents a document content block.
	// https://platform.claude.com/docs/en/api/messages#document_block_param
	DocumentBlockParam struct {
		Type         string                `json:"type"` // Always "document".
		Source       DocumentSource        `json:"source"`
		CacheControl *CacheControl         `json:"cache_control,omitempty"`
		Citations    *CitationsConfigParam `json:"citations,omitempty"`
		Context      string                `json:"context,omitempty"`
		Title        string                `json:"title,omitempty"`
	}

	// SearchResultBlockParam represents a search result content block.
	// https://platform.claude.com/docs/en/api/messages#search_result_block_param
	SearchResultBlockParam struct {
		Type         string                `json:"type"` // Always "search_result".
		Content      []TextBlockParam      `json:"content"`
		Source       string                `json:"source"`
		Title        string                `json:"title"`
		CacheControl *CacheControl         `json:"cache_control,omitempty"`
		Citations    *CitationsConfigParam `json:"citations,omitempty"`
	}

	// ThinkingBlockParam represents a thinking content block in a request.
	// https://platform.claude.com/docs/en/api/messages#thinking_block_param
	ThinkingBlockParam struct {
		Type      string `json:"type"` // Always "thinking".
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}

	// RedactedThinkingBlockParam represents a redacted thinking content block.
	// https://platform.claude.com/docs/en/api/messages#redacted_thinking_block_param
	RedactedThinkingBlockParam struct {
		Type string `json:"type"` // Always "redacted_thinking".
		Data string `json:"data"`
	}

	// ToolUseBlockParam represents a tool use content block in a request.
	// https://platform.claude.com/docs/en/api/messages#tool_use_block_param
	ToolUseBlockParam struct {
		Type         string         `json:"type"` // Always "tool_use".
		ID           string         `json:"id"`
		Name         string         `json:"name"`
		Input        map[string]any `json:"input"`
		CacheControl *CacheControl  `json:"cache_control,omitempty"`
	}

	// ToolResultBlockParam represents a tool result content block.
	// https://platform.claude.com/docs/en/api/messages#tool_result_block_param
	ToolResultBlockParam struct {
		Type         string             `json:"type"` // Always "tool_result".
		ToolUseID    string             `json:"tool_use_id"`
		Content      *ToolResultContent `json:"content,omitempty"` // string or array of content blocks.
		IsError      bool               `json:"is_error,omitempty"`
		CacheControl *CacheControl      `json:"cache_control,omitempty"`
	}

	// ToolResultContent represents the content of a tool result block,
	// which can be a string or an array of text, image, search result, or document blocks.
	// https://platform.claude.com/docs/en/api/messages#tool_result_block_param
	ToolResultContent struct {
		Text  string                  // Non-empty if this is plain text content.
		Array []ToolResultContentItem // Non-empty if this is array content.
	}

	// ToolResultContentItem is a single content block in a tool result array.
	// https://platform.claude.com/docs/en/api/messages#tool_result_block_param
	ToolResultContentItem struct {
		Text         *TextBlockParam
		Image        *ImageBlockParam
		SearchResult *SearchResultBlockParam
		Document     *DocumentBlockParam
	}

	// ServerToolUseBlockParam represents a server tool use content block.
	// https://platform.claude.com/docs/en/api/messages#server_tool_use_block_param
	ServerToolUseBlockParam struct {
		Type         string         `json:"type"` // Always "server_tool_use".
		ID           string         `json:"id"`
		Name         string         `json:"name"`
		Input        map[string]any `json:"input"`
		CacheControl *CacheControl  `json:"cache_control,omitempty"`
	}

	// WebSearchToolResultBlockParam represents a web search tool result content block.
	// https://platform.claude.com/docs/en/api/messages#web_search_tool_result_block_param
	WebSearchToolResultBlockParam struct {
		Type         string                     `json:"type"` // Always "web_search_tool_result".
		ToolUseID    string                     `json:"tool_use_id"`
		Content      WebSearchToolResultContent `json:"content"`
		CacheControl *CacheControl              `json:"cache_control,omitempty"`
	}
)

// Content block type constants used by ContentBlockParam and MessagesContentBlock.
const (
	contentBlockTypeText                = "text"
	contentBlockTypeImage               = "image"
	contentBlockTypeDocument            = "document"
	contentBlockTypeSearchResult        = "search_result"
	contentBlockTypeThinking            = "thinking"
	contentBlockTypeRedactedThinking    = "redacted_thinking"
	contentBlockTypeToolUse             = "tool_use"
	contentBlockTypeToolResult          = "tool_result"
	contentBlockTypeServerToolUse       = "server_tool_use"
	contentBlockTypeWebSearchToolResult = "web_search_tool_result"
)

func (m *ContentBlockParam) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in message content block")
	}
	switch typ.String() {
	case contentBlockTypeText:
		var blockParam TextBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal text blockParam: %w", err)
		}
		m.Text = &blockParam
	case contentBlockTypeImage:
		var blockParam ImageBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal image blockParam: %w", err)
		}
		m.Image = &blockParam
	case contentBlockTypeDocument:
		var blockParam DocumentBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal document blockParam: %w", err)
		}
		m.Document = &blockParam
	case contentBlockTypeSearchResult:
		var blockParam SearchResultBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal search result blockParam: %w", err)
		}
		m.SearchResult = &blockParam
	case contentBlockTypeThinking:
		var blockParam ThinkingBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal thinking blockParam: %w", err)
		}
		m.Thinking = &blockParam
	case contentBlockTypeRedactedThinking:
		var blockParam RedactedThinkingBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal redacted thinking blockParam: %w", err)
		}
		m.RedactedThinking = &blockParam
	case contentBlockTypeToolUse:
		var blockParam ToolUseBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal tool use blockParam: %w", err)
		}
		m.ToolUse = &blockParam
	case contentBlockTypeToolResult:
		var blockParam ToolResultBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal tool result blockParam: %w", err)
		}
		m.ToolResult = &blockParam
	case contentBlockTypeServerToolUse:
		var blockParam ServerToolUseBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal server tool use blockParam: %w", err)
		}
		m.ServerToolUse = &blockParam
	case contentBlockTypeWebSearchToolResult:
		var blockParam WebSearchToolResultBlockParam
		if err := json.Unmarshal(data, &blockParam); err != nil {
			return fmt.Errorf("failed to unmarshal web search tool result blockParam: %w", err)
		}
		m.WebSearchToolResult = &blockParam
	default:
		// Ignore unknown types for forward compatibility.
		return nil
	}
	return nil
}

func (m *ContentBlockParam) MarshalJSON() ([]byte, error) {
	if m.Text != nil {
		return json.Marshal(m.Text)
	}
	if m.Image != nil {
		return json.Marshal(m.Image)
	}
	if m.Document != nil {
		return json.Marshal(m.Document)
	}
	if m.SearchResult != nil {
		return json.Marshal(m.SearchResult)
	}
	if m.Thinking != nil {
		return json.Marshal(m.Thinking)
	}
	if m.RedactedThinking != nil {
		return json.Marshal(m.RedactedThinking)
	}
	if m.ToolUse != nil {
		return json.Marshal(m.ToolUse)
	}
	if m.ToolResult != nil {
		return json.Marshal(m.ToolResult)
	}
	if m.ServerToolUse != nil {
		return json.Marshal(m.ServerToolUse)
	}
	if m.WebSearchToolResult != nil {
		return json.Marshal(m.WebSearchToolResult)
	}
	return nil, fmt.Errorf("content block param must have a defined type")
}

func (c *ToolResultContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		return nil
	}
	var array []ToolResultContentItem
	if err := json.Unmarshal(data, &array); err == nil {
		c.Array = array
		return nil
	}
	return fmt.Errorf("tool result content must be either text or array of content blocks")
}

func (c *ToolResultContent) MarshalJSON() ([]byte, error) {
	if c.Text != "" {
		return json.Marshal(c.Text)
	}
	if len(c.Array) > 0 {
		return json.Marshal(c.Array)
	}
	return nil, fmt.Errorf("tool result content must have either text or array")
}

func (item *ToolResultContentItem) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in tool result content item")
	}
	switch typ.String() {
	case contentBlockTypeText:
		var block TextBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal text block in tool result: %w", err)
		}
		item.Text = &block
	case contentBlockTypeImage:
		var block ImageBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal image block in tool result: %w", err)
		}
		item.Image = &block
	case contentBlockTypeSearchResult:
		var block SearchResultBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal search result block in tool result: %w", err)
		}
		item.SearchResult = &block
	case contentBlockTypeDocument:
		var block DocumentBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal document block in tool result: %w", err)
		}
		item.Document = &block
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (item ToolResultContentItem) MarshalJSON() ([]byte, error) {
	if item.Text != nil {
		return json.Marshal(item.Text)
	}
	if item.Image != nil {
		return json.Marshal(item.Image)
	}
	if item.SearchResult != nil {
		return json.Marshal(item.SearchResult)
	}
	if item.Document != nil {
		return json.Marshal(item.Document)
	}
	return nil, fmt.Errorf("tool result content item must have a defined type")
}

// CacheControl represents a cache control configuration.
// https://platform.claude.com/docs/en/api/messages#cache_control_ephemeral
type CacheControl struct {
	Ephemeral *CacheControlEphemeral
}

// CacheControlEphemeral represents an ephemeral cache control breakpoint.
// https://platform.claude.com/docs/en/api/messages#cache_control_ephemeral
type CacheControlEphemeral struct {
	// Type is always "ephemeral".
	Type string `json:"type"`
	// TTL is the time-to-live for the cache entry. Valid values: "5m" or "1h". Defaults to "5m".
	TTL *string `json:"ttl,omitempty"`
}

const cacheControlTypeEphemeral = "ephemeral"

func (c *CacheControl) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in cache control")
	}
	switch typ.String() {
	case cacheControlTypeEphemeral:
		var cc CacheControlEphemeral
		if err := json.Unmarshal(data, &cc); err != nil {
			return fmt.Errorf("failed to unmarshal cache control ephemeral: %w", err)
		}
		c.Ephemeral = &cc
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (c *CacheControl) MarshalJSON() ([]byte, error) {
	if c.Ephemeral != nil {
		return json.Marshal(c.Ephemeral)
	}
	return nil, fmt.Errorf("cache control must have a defined type")
}

type (
	// ImageSource represents the source of an image content block.
	// https://platform.claude.com/docs/en/api/messages#image_block_param
	ImageSource struct {
		Base64 *Base64ImageSource
		URL    *URLImageSource
	}

	// Base64ImageSource represents a base64-encoded image.
	// https://platform.claude.com/docs/en/api/messages#base64_image_source
	Base64ImageSource struct {
		// Type is always "base64".
		Type string `json:"type"`
		// MediaType is the MIME type of the image. Valid values: "image/jpeg", "image/png", "image/gif", "image/webp".
		MediaType string `json:"media_type"`
		// Data is the base64-encoded image data.
		Data string `json:"data"`
	}

	// URLImageSource represents an image from a URL.
	// https://platform.claude.com/docs/en/api/messages#url_image_source
	URLImageSource struct {
		// Type is always "url".
		Type string `json:"type"`
		// URL is the URL of the image.
		URL string `json:"url"`
	}
)

// Image source type constants.
const (
	imageSourceTypeBase64 = "base64"
	imageSourceTypeURL    = "url"
)

func (s *ImageSource) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in image source")
	}
	switch typ.String() {
	case imageSourceTypeBase64:
		var src Base64ImageSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal base64 image source: %w", err)
		}
		s.Base64 = &src
	case imageSourceTypeURL:
		var src URLImageSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal URL image source: %w", err)
		}
		s.URL = &src
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (s ImageSource) MarshalJSON() ([]byte, error) {
	if s.Base64 != nil {
		return json.Marshal(s.Base64)
	}
	if s.URL != nil {
		return json.Marshal(s.URL)
	}
	return nil, fmt.Errorf("image source must have a defined type")
}

type (
	// DocumentSource represents the source of a document content block.
	// https://platform.claude.com/docs/en/api/messages#document_block_param
	DocumentSource struct {
		Base64PDF    *Base64PDFSource
		PlainText    *PlainTextSource
		URL          *URLPDFSource
		ContentBlock *ContentBlockSource
	}

	// Base64PDFSource represents a base64-encoded PDF document.
	// https://platform.claude.com/docs/en/api/messages#base64_pdf_source
	Base64PDFSource struct {
		// Type is always "base64".
		Type string `json:"type"`
		// MediaType is always "application/pdf".
		MediaType string `json:"media_type"`
		// Data is the base64-encoded PDF data.
		Data string `json:"data"`
	}

	// PlainTextSource represents a plain text document.
	// https://platform.claude.com/docs/en/api/messages#plain_text_source
	PlainTextSource struct {
		// Type is always "text".
		Type string `json:"type"`
		// MediaType is always "text/plain".
		MediaType string `json:"media_type"`
		// Data is the plain text content.
		Data string `json:"data"`
	}

	// URLPDFSource represents a PDF document from a URL.
	// https://platform.claude.com/docs/en/api/messages#url_pdf_source
	URLPDFSource struct {
		// Type is always "url".
		Type string `json:"type"`
		// URL is the URL of the PDF document.
		URL string `json:"url"`
	}

	// ContentBlockSource represents a document sourced from content blocks.
	// https://platform.claude.com/docs/en/api/messages#content_block_source
	ContentBlockSource struct {
		// Type is always "content".
		Type string `json:"type"`
		// Content is the content of the document, either a string or an array of content blocks.
		Content ContentBlockSourceContent `json:"content"`
	}

	// ContentBlockSourceContent is the content of a ContentBlockSource, which can be
	// a string or an array of text/image content blocks.
	ContentBlockSourceContent struct {
		Text  string                   // Non-empty if this is plain string content.
		Array []ContentBlockSourceItem // Non-empty if this is array content.
	}

	// ContentBlockSourceItem is a single content block in a ContentBlockSource array.
	// It can be a text or image block.
	ContentBlockSourceItem struct {
		Text  *TextBlockParam
		Image *ImageBlockParam
	}
)

// Document source type constants.
const (
	documentSourceTypeBase64  = "base64"
	documentSourceTypeText    = "text"
	documentSourceTypeURL     = "url"
	documentSourceTypeContent = "content"
)

func (s *DocumentSource) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in document source")
	}
	switch typ.String() {
	case documentSourceTypeBase64:
		var src Base64PDFSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal base64 PDF source: %w", err)
		}
		s.Base64PDF = &src
	case documentSourceTypeText:
		var src PlainTextSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal plain text source: %w", err)
		}
		s.PlainText = &src
	case documentSourceTypeURL:
		var src URLPDFSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal URL PDF source: %w", err)
		}
		s.URL = &src
	case documentSourceTypeContent:
		var src ContentBlockSource
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("failed to unmarshal content block source: %w", err)
		}
		s.ContentBlock = &src
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (s DocumentSource) MarshalJSON() ([]byte, error) {
	if s.Base64PDF != nil {
		return json.Marshal(s.Base64PDF)
	}
	if s.PlainText != nil {
		return json.Marshal(s.PlainText)
	}
	if s.URL != nil {
		return json.Marshal(s.URL)
	}
	if s.ContentBlock != nil {
		return json.Marshal(s.ContentBlock)
	}
	return nil, fmt.Errorf("document source must have a defined type")
}

func (c *ContentBlockSourceContent) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first.
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		return nil
	}
	// Try to unmarshal as array of content blocks.
	var array []ContentBlockSourceItem
	if err := json.Unmarshal(data, &array); err == nil {
		c.Array = array
		return nil
	}
	return fmt.Errorf("content block source content must be either text or array")
}

func (c ContentBlockSourceContent) MarshalJSON() ([]byte, error) {
	if c.Text != "" {
		return json.Marshal(c.Text)
	}
	if len(c.Array) > 0 {
		return json.Marshal(c.Array)
	}
	return nil, fmt.Errorf("content block source content must have either text or array")
}

func (item *ContentBlockSourceItem) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in content block source item")
	}
	switch typ.String() {
	case contentBlockTypeText:
		var block TextBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal text block in content block source: %w", err)
		}
		item.Text = &block
	case contentBlockTypeImage:
		var block ImageBlockParam
		if err := json.Unmarshal(data, &block); err != nil {
			return fmt.Errorf("failed to unmarshal image block in content block source: %w", err)
		}
		item.Image = &block
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (item ContentBlockSourceItem) MarshalJSON() ([]byte, error) {
	if item.Text != nil {
		return json.Marshal(item.Text)
	}
	if item.Image != nil {
		return json.Marshal(item.Image)
	}
	return nil, fmt.Errorf("content block source item must have a defined type")
}

// CitationsConfigParam enables or disables citations for a document or search result block.
// https://platform.claude.com/docs/en/api/messages#citations_config_param
type CitationsConfigParam struct {
	// Enabled indicates whether citations are enabled.
	Enabled *bool `json:"enabled,omitempty"`
}

type (
	// TextCitation represents a single citation in a text block, used in both requests
	// and responses.
	// https://platform.claude.com/docs/en/api/messages#text_citation_param
	TextCitation struct {
		CharLocation            *CitationCharLocation
		PageLocation            *CitationPageLocation
		ContentBlockLocation    *CitationContentBlockLocation
		WebSearchResultLocation *CitationWebSearchResultLocation
		SearchResultLocation    *CitationSearchResultLocation
	}

	// CitationCharLocation represents a citation with character-level location in a document.
	// https://platform.claude.com/docs/en/api/messages#citation_char_location
	CitationCharLocation struct {
		// Type is always "char_location".
		Type string `json:"type"`
		// CitedText is the exact text being cited.
		CitedText string `json:"cited_text"`
		// DocumentIndex is the index of the document being cited in the documents array.
		DocumentIndex int `json:"document_index"`
		// DocumentTitle is the title of the cited document.
		DocumentTitle *string `json:"document_title,omitempty"`
		// StartCharIndex is the start character index of the citation within the document.
		StartCharIndex int `json:"start_char_index"`
		// EndCharIndex is the end character index of the citation within the document.
		EndCharIndex int `json:"end_char_index"`
	}

	// CitationPageLocation represents a citation with page-level location in a document.
	// https://platform.claude.com/docs/en/api/messages#citation_page_location
	CitationPageLocation struct {
		// Type is always "page_location".
		Type string `json:"type"`
		// CitedText is the exact text being cited.
		CitedText string `json:"cited_text"`
		// DocumentIndex is the index of the document being cited in the documents array.
		DocumentIndex int `json:"document_index"`
		// DocumentTitle is the title of the cited document.
		DocumentTitle *string `json:"document_title,omitempty"`
		// StartPageNumber is the 1-indexed start page number of the citation.
		StartPageNumber int `json:"start_page_number"`
		// EndPageNumber is the 1-indexed end page number of the citation.
		EndPageNumber int `json:"end_page_number"`
	}

	// CitationContentBlockLocation represents a citation with content-block-level location.
	// https://platform.claude.com/docs/en/api/messages#citation_content_block_location
	CitationContentBlockLocation struct {
		// Type is always "content_block_location".
		Type string `json:"type"`
		// CitedText is the exact text being cited.
		CitedText string `json:"cited_text"`
		// DocumentIndex is the index of the document being cited in the documents array.
		DocumentIndex int `json:"document_index"`
		// DocumentTitle is the title of the cited document.
		DocumentTitle *string `json:"document_title,omitempty"`
		// StartBlockIndex is the start block index of the citation within the document.
		StartBlockIndex int `json:"start_block_index"`
		// EndBlockIndex is the end block index of the citation within the document.
		EndBlockIndex int `json:"end_block_index"`
	}

	// CitationWebSearchResultLocation represents a citation from a web search result.
	// https://platform.claude.com/docs/en/api/messages#citation_web_search_result_location
	CitationWebSearchResultLocation struct {
		// Type is always "web_search_result_location".
		Type string `json:"type"`
		// CitedText is the exact text being cited.
		CitedText string `json:"cited_text"`
		// EncryptedIndex is the encrypted index of the web search result.
		EncryptedIndex string `json:"encrypted_index"`
		// Title is the title of the web page.
		Title string `json:"title,omitempty"`
		// URL is the URL of the web page.
		URL string `json:"url"`
	}

	// CitationSearchResultLocation represents a citation from a search result block.
	// https://platform.claude.com/docs/en/api/messages#citation_search_result_location
	CitationSearchResultLocation struct {
		// Type is always "search_result_location".
		Type string `json:"type"`
		// CitedText is the exact text being cited.
		CitedText string `json:"cited_text"`
		// Title is the title of the search result.
		Title string `json:"title,omitempty"`
		// Source is the source URL or identifier of the search result.
		Source string `json:"source"`
		// StartBlockIndex is the start block index within the search result.
		StartBlockIndex int `json:"start_block_index"`
		// EndBlockIndex is the end block index within the search result.
		EndBlockIndex int `json:"end_block_index"`
		// SearchResultIndex is the index of the search result in the search results array.
		SearchResultIndex int `json:"search_result_index"`
	}
)

// Citation type constants.
const (
	citationTypeCharLocation            = "char_location"
	citationTypePageLocation            = "page_location"
	citationTypeContentBlockLocation    = "content_block_location"
	citationTypeWebSearchResultLocation = "web_search_result_location"
	citationTypeSearchResultLocation    = "search_result_location"
)

func (c *TextCitation) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in text citation")
	}
	switch typ.String() {
	case citationTypeCharLocation:
		var citation CitationCharLocation
		if err := json.Unmarshal(data, &citation); err != nil {
			return fmt.Errorf("failed to unmarshal char location citation: %w", err)
		}
		c.CharLocation = &citation
	case citationTypePageLocation:
		var citation CitationPageLocation
		if err := json.Unmarshal(data, &citation); err != nil {
			return fmt.Errorf("failed to unmarshal page location citation: %w", err)
		}
		c.PageLocation = &citation
	case citationTypeContentBlockLocation:
		var citation CitationContentBlockLocation
		if err := json.Unmarshal(data, &citation); err != nil {
			return fmt.Errorf("failed to unmarshal content block location citation: %w", err)
		}
		c.ContentBlockLocation = &citation
	case citationTypeWebSearchResultLocation:
		var citation CitationWebSearchResultLocation
		if err := json.Unmarshal(data, &citation); err != nil {
			return fmt.Errorf("failed to unmarshal web search result location citation: %w", err)
		}
		c.WebSearchResultLocation = &citation
	case citationTypeSearchResultLocation:
		var citation CitationSearchResultLocation
		if err := json.Unmarshal(data, &citation); err != nil {
			return fmt.Errorf("failed to unmarshal search result location citation: %w", err)
		}
		c.SearchResultLocation = &citation
	default:
		// Ignore unknown types for forward compatibility.
	}
	return nil
}

func (c *TextCitation) MarshalJSON() ([]byte, error) {
	if c.CharLocation != nil {
		return json.Marshal(c.CharLocation)
	}
	if c.PageLocation != nil {
		return json.Marshal(c.PageLocation)
	}
	if c.ContentBlockLocation != nil {
		return json.Marshal(c.ContentBlockLocation)
	}
	if c.WebSearchResultLocation != nil {
		return json.Marshal(c.WebSearchResultLocation)
	}
	if c.SearchResultLocation != nil {
		return json.Marshal(c.SearchResultLocation)
	}
	return nil, fmt.Errorf("text citation must have a defined type")
}

type (
	// WebSearchToolResultContent is the content of a web search tool result block.
	// It can be an array of web search results or a single error.
	WebSearchToolResultContent struct {
		Results []WebSearchResult         // Non-nil if content is an array of results.
		Error   *WebSearchToolResultError // Non-nil if content is an error.
	}

	// WebSearchResult represents a single web search result.
	// https://platform.claude.com/docs/en/api/messages#web_search_result
	WebSearchResult struct {
		// Type is always "web_search_result".
		Type string `json:"type"`
		// Title is the title of the web page.
		Title string `json:"title"`
		// URL is the URL of the web page.
		URL string `json:"url"`
		// EncryptedContent is the encrypted content of the web page.
		EncryptedContent string `json:"encrypted_content"`
		// PageAge is an optional age indicator for the page (e.g. "2 days ago").
		PageAge *string `json:"page_age,omitempty"`
	}

	// WebSearchToolResultError represents an error in a web search tool result.
	// https://platform.claude.com/docs/en/api/messages#web_search_tool_result_error
	WebSearchToolResultError struct {
		// Type is always "web_search_tool_result_error".
		Type string `json:"type"`
		// ErrorCode is the error code. Valid values: "invalid_tool_input", "unavailable",
		// "max_uses_exceeded", "too_many_requests", "query_too_long", "request_too_large".
		ErrorCode string `json:"error_code"`
	}
)

func (w *WebSearchToolResultContent) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as array of WebSearchResult first.
	var results []WebSearchResult
	if err := json.Unmarshal(data, &results); err == nil {
		w.Results = results
		return nil
	}
	// Try to unmarshal as a single WebSearchToolResultError.
	var wsError WebSearchToolResultError
	if err := json.Unmarshal(data, &wsError); err == nil {
		w.Error = &wsError
		return nil
	}
	return fmt.Errorf("web search tool result content must be an array of results or an error")
}

func (w WebSearchToolResultContent) MarshalJSON() ([]byte, error) {
	if w.Results != nil {
		return json.Marshal(w.Results)
	}
	if w.Error != nil {
		return json.Marshal(w.Error)
	}
	return nil, fmt.Errorf("web search tool result content must have either results or an error")
}

// MessagesMetadata represents the metadata for the Anthropic Messages API request.
// https://docs.claude.com/en/api/messages#body-metadata
type MessagesMetadata struct {
	// UserID is an optional user identifier for tracking purposes.
	UserID *string `json:"user_id,omitempty"`
}

// MessageServiceTier represents the service tier for the Anthropic Messages API request.
//
// https://docs.claude.com/en/api/messages#body-service-tier
type MessageServiceTier string

const (
	MessageServiceTierAuto         MessageServiceTier = "auto"
	MessageServiceTierStandardOnly MessageServiceTier = "standard_only"
)

// Container represents a container identifier for reuse across requests.
// This became a beta status so it is not implemented for now.
// https://platform.claude.com/docs/en/api/beta/messages/create
type Container any // TODO when we need it for observability, etc.

type (
	// ToolUnion represents a tool available to the model.
	// https://platform.claude.com/docs/en/api/messages#tool_union
	ToolUnion struct {
		Tool                   *Tool
		BashTool               *BashTool
		TextEditorTool20250124 *TextEditorTool20250124
		TextEditorTool20250429 *TextEditorTool20250429
		TextEditorTool20250728 *TextEditorTool20250728
		WebSearchTool          *WebSearchTool
	}

	// Tool represents a custom tool definition.
	// https://platform.claude.com/docs/en/api/messages#tool
	Tool struct {
		Type string `json:"type"` // Always "custom".
		Name string `json:"name"`
		// InputSchema is the JSON schema of the tool input, carried as-is. Modeling its
		// keywords would drop the ones left out, such as "additionalProperties", which
		// backends require to enable strict schema adherence.
		InputSchema  json.RawMessage `json:"input_schema"`
		CacheControl *CacheControl   `json:"cache_control,omitempty"`
		Description  string          `json:"description,omitempty"`
	}

	// BashTool represents the bash tool for computer use.
	// https://platform.claude.com/docs/en/api/messages#tool_bash_20250124
	BashTool struct {
		Type         string        `json:"type"` // Always "bash_20250124".
		Name         string        `json:"name"` // Always "bash".
		CacheControl *CacheControl `json:"cache_control,omitempty"`
	}

	// TextEditorTool20250124 represents the text editor tool (v1).
	// https://platform.claude.com/docs/en/api/messages#tool_text_editor_20250124
	TextEditorTool20250124 struct {
		Type         string        `json:"type"` // Always "text_editor_20250124".
		Name         string        `json:"name"` // Always "str_replace_editor".
		CacheControl *CacheControl `json:"cache_control,omitempty"`
	}

	// TextEditorTool20250429 represents the text editor tool (v2).
	// https://platform.claude.com/docs/en/api/messages#tool_text_editor_20250429
	TextEditorTool20250429 struct {
		Type         string        `json:"type"` // Always "text_editor_20250429".
		Name         string        `json:"name"` // Always "str_replace_based_edit_tool".
		CacheControl *CacheControl `json:"cache_control,omitempty"`
	}

	// TextEditorTool20250728 represents the text editor tool (v3).
	// https://platform.claude.com/docs/en/api/messages#tool_text_editor_20250728
	TextEditorTool20250728 struct {
		Type          string        `json:"type"` // Always "text_editor_20250728".
		Name          string        `json:"name"` // Always "str_replace_based_edit_tool".
		MaxCharacters *float64      `json:"max_characters,omitempty"`
		CacheControl  *CacheControl `json:"cache_control,omitempty"`
	}

	// WebSearchTool represents the web search tool.
	// https://platform.claude.com/docs/en/api/messages#web_search_tool_20250305
	WebSearchTool struct {
		Type           string             `json:"type"` // Always "web_search_20250305".
		Name           string             `json:"name"` // Always "web_search".
		AllowedDomains []string           `json:"allowed_domains,omitempty"`
		BlockedDomains []string           `json:"blocked_domains,omitempty"`
		MaxUses        *float64           `json:"max_uses,omitempty"`
		UserLocation   *WebSearchLocation `json:"user_location,omitempty"`
		CacheControl   *CacheControl      `json:"cache_control,omitempty"`
	}

	// WebSearchLocation represents the user location for the web search tool.
	WebSearchLocation struct {
		Type     string `json:"type"` // Always "approximate".
		City     string `json:"city,omitempty"`
		Country  string `json:"country,omitempty"`
		Region   string `json:"region,omitempty"`
		Timezone string `json:"timezone,omitempty"`
	}
)

// Tool type constants used by ToolUnion.
const (
	toolTypeCustom             = "custom"
	toolTypeBash20250124       = "bash_20250124"
	toolTypeTextEditor20250124 = "text_editor_20250124"
	toolTypeTextEditor20250429 = "text_editor_20250429"
	toolTypeTextEditor20250728 = "text_editor_20250728"
	toolTypeWebSearch20250305  = "web_search_20250305"
)

func (t *ToolUnion) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	typStr := toolTypeCustom
	if typ.Exists() && typ.String() != "" {
		typStr = typ.String()
	}
	switch typStr {
	case toolTypeCustom:
		var tool Tool
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal tool: %w", err)
		}
		t.Tool = &tool
	case toolTypeBash20250124:
		var tool BashTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal bash tool: %w", err)
		}
		t.BashTool = &tool
	case toolTypeTextEditor20250124:
		var tool TextEditorTool20250124
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal text editor tool: %w", err)
		}
		t.TextEditorTool20250124 = &tool
	case toolTypeTextEditor20250429:
		var tool TextEditorTool20250429
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal text editor tool: %w", err)
		}
		t.TextEditorTool20250429 = &tool
	case toolTypeTextEditor20250728:
		var tool TextEditorTool20250728
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal text editor tool: %w", err)
		}
		t.TextEditorTool20250728 = &tool
	case toolTypeWebSearch20250305:
		var tool WebSearchTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("failed to unmarshal web search tool: %w", err)
		}
		t.WebSearchTool = &tool
	default:
		// Ignore unknown types for forward compatibility.
		return nil
	}
	return nil
}

func (t *ToolUnion) MarshalJSON() ([]byte, error) {
	if t.Tool != nil {
		return json.Marshal(t.Tool)
	}
	if t.BashTool != nil {
		return json.Marshal(t.BashTool)
	}
	if t.TextEditorTool20250124 != nil {
		return json.Marshal(t.TextEditorTool20250124)
	}
	if t.TextEditorTool20250429 != nil {
		return json.Marshal(t.TextEditorTool20250429)
	}
	if t.TextEditorTool20250728 != nil {
		return json.Marshal(t.TextEditorTool20250728)
	}
	if t.WebSearchTool != nil {
		return json.Marshal(t.WebSearchTool)
	}
	return nil, fmt.Errorf("tool union must have a defined type")
}

type (
	// ToolChoice represents the tool choice for the model.
	// https://platform.claude.com/docs/en/api/messages#body-tool-choice
	ToolChoice struct {
		Auto *ToolChoiceAuto
		Any  *ToolChoiceAny
		Tool *ToolChoiceTool
		None *ToolChoiceNone
	}

	// ToolChoiceAuto lets the model automatically decide whether to use tools.
	// https://platform.claude.com/docs/en/api/messages#tool_choice_auto
	ToolChoiceAuto struct {
		Type                   string `json:"type"` // Always "auto".
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	}

	// ToolChoiceAny forces the model to use any available tool.
	// https://platform.claude.com/docs/en/api/messages#tool_choice_any
	ToolChoiceAny struct {
		Type                   string `json:"type"` // Always "any".
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	}

	// ToolChoiceTool forces the model to use the specified tool.
	// https://platform.claude.com/docs/en/api/messages#tool_choice_tool
	ToolChoiceTool struct {
		Type                   string `json:"type"` // Always "tool".
		Name                   string `json:"name"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	}

	// ToolChoiceNone prevents the model from using any tools.
	// https://platform.claude.com/docs/en/api/messages#tool_choice_none
	ToolChoiceNone struct {
		Type string `json:"type"` // Always "none".
	}
)

// Tool choice type constants used by ToolChoice.
const (
	toolChoiceTypeAuto = "auto"
	toolChoiceTypeAny  = "any"
	toolChoiceTypeTool = "tool"
	toolChoiceTypeNone = "none"
)

func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in tool choice")
	}
	switch typ.String() {
	case toolChoiceTypeAuto:
		var toolChoice ToolChoiceAuto
		if err := json.Unmarshal(data, &toolChoice); err != nil {
			return fmt.Errorf("failed to unmarshal tool choice auto: %w", err)
		}
		tc.Auto = &toolChoice
	case toolChoiceTypeAny:
		var toolChoice ToolChoiceAny
		if err := json.Unmarshal(data, &toolChoice); err != nil {
			return fmt.Errorf("failed to unmarshal tool choice any: %w", err)
		}
		tc.Any = &toolChoice
	case toolChoiceTypeTool:
		var toolChoice ToolChoiceTool
		if err := json.Unmarshal(data, &toolChoice); err != nil {
			return fmt.Errorf("failed to unmarshal tool choice tool: %w", err)
		}
		tc.Tool = &toolChoice
	case toolChoiceTypeNone:
		var toolChoice ToolChoiceNone
		if err := json.Unmarshal(data, &toolChoice); err != nil {
			return fmt.Errorf("failed to unmarshal tool choice none: %w", err)
		}
		tc.None = &toolChoice
	default:
		// Ignore unknown types for forward compatibility.
		return nil
	}
	return nil
}

func (tc *ToolChoice) MarshalJSON() ([]byte, error) {
	if tc.Auto != nil {
		return json.Marshal(tc.Auto)
	}
	if tc.Any != nil {
		return json.Marshal(tc.Any)
	}
	if tc.Tool != nil {
		return json.Marshal(tc.Tool)
	}
	if tc.None != nil {
		return json.Marshal(tc.None)
	}
	return nil, fmt.Errorf("tool choice must have a defined type")
}

type (
	// Thinking represents the configuration for the model's "thinking" behavior.
	// This is not to be confused with the thinking block that is part of the response message's contentblock
	// https://platform.claude.com/docs/en/api/messages#body-thinking
	Thinking struct {
		Enabled  *ThinkingEnabled
		Disabled *ThinkingDisabled
		Adaptive *ThinkingAdaptive
	}

	// ThinkingEnabled enables extended thinking with a token budget.
	// https://platform.claude.com/docs/en/api/messages#thinking_config_enabled
	ThinkingEnabled struct {
		Type         string  `json:"type"`              // Always "enabled".
		BudgetTokens float64 `json:"budget_tokens"`     // Must be >= 1024 and < max_tokens.
		Display      string  `json:"display,omitempty"` // "summarized" or "omitted".
	}

	// ThinkingDisabled disables extended thinking.
	// https://platform.claude.com/docs/en/api/messages#thinking_config_disabled
	ThinkingDisabled struct {
		Type string `json:"type"` // Always "disabled".
	}

	// ThinkingAdaptive lets the model decide whether to use extended thinking.
	// https://platform.claude.com/docs/en/api/messages#thinking_config_adaptive
	ThinkingAdaptive struct {
		Type    string `json:"type"`              // Always "adaptive".
		Display string `json:"display,omitempty"` // "summarized" or "omitted".
	}
)

// Thinking config type constants used by Thinking.
const (
	thinkingConfigTypeEnabled  = "enabled"
	thinkingConfigTypeDisabled = "disabled"
	thinkingConfigTypeAdaptive = "adaptive"
)

func (t *Thinking) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in thinking config")
	}
	switch typ.String() {
	case thinkingConfigTypeEnabled:
		var thinking ThinkingEnabled
		if err := json.Unmarshal(data, &thinking); err != nil {
			return fmt.Errorf("failed to unmarshal thinking enabled: %w", err)
		}
		t.Enabled = &thinking
	case thinkingConfigTypeDisabled:
		var thinking ThinkingDisabled
		if err := json.Unmarshal(data, &thinking); err != nil {
			return fmt.Errorf("failed to unmarshal thinking disabled: %w", err)
		}
		t.Disabled = &thinking
	case thinkingConfigTypeAdaptive:
		var thinking ThinkingAdaptive
		if err := json.Unmarshal(data, &thinking); err != nil {
			return fmt.Errorf("failed to unmarshal thinking adaptive: %w", err)
		}
		t.Adaptive = &thinking
	default:
		// Ignore unknown types for forward compatibility.
		return nil
	}
	return nil
}

func (t *Thinking) MarshalJSON() ([]byte, error) {
	if t.Enabled != nil {
		return json.Marshal(t.Enabled)
	}
	if t.Disabled != nil {
		return json.Marshal(t.Disabled)
	}
	if t.Adaptive != nil {
		return json.Marshal(t.Adaptive)
	}
	return nil, fmt.Errorf("thinking config must have a defined type")
}

// SystemPrompt represents a system prompt to guide the model's behavior.
// https://docs.claude.com/en/api/messages#body-system
type SystemPrompt struct {
	Text  string
	Texts []TextBlockParam
}

func (s *SystemPrompt) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first.
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		s.Text = text
		return nil
	}

	// Try to unmarshal as array of TextBlockParam.
	var texts []TextBlockParam
	if err := json.Unmarshal(data, &texts); err == nil {
		s.Texts = texts
		return nil
	}
	return fmt.Errorf("system prompt must be either string or array of text blocks")
}

func (s *SystemPrompt) MarshalJSON() ([]byte, error) {
	if s.Text != "" {
		return json.Marshal(s.Text)
	}
	if len(s.Texts) > 0 {
		return json.Marshal(s.Texts)
	}
	return nil, fmt.Errorf("system prompt must have either text or texts")
}

// MCPServer represents an MCP server.
// This became a beta status so it is not implemented for now.
// https://platform.claude.com/docs/en/api/beta/messages/create
type MCPServer any // TODO when we need it for observability, etc.

// ContextManagement represents the context management configuration.
// This became a beta status so it is not implemented for now.
// https://platform.claude.com/docs/en/api/beta/messages/create
type ContextManagement any // TODO when we need it for observability, etc.

// MessagesResponse represents a response from the Anthropic Messages API.
// https://docs.claude.com/en/api/messages
type MessagesResponse struct {
	// ID is the unique identifier for the response.
	// https://docs.claude.com/en/api/messages#response-id
	ID string `json:"id"`
	// Type is the type of the response.
	// This is always "messages".
	//
	// https://docs.claude.com/en/api/messages#response-type
	Type ConstantMessagesResponseTypeMessages `json:"type"`
	// Role is the role of the message in the response.
	// This is always "assistant".
	//
	// https://docs.claude.com/en/api/messages#response-role
	Role ConstantMessagesResponseRoleAssistant `json:"role"`
	// Content is the content of the message in the response.
	// https://docs.claude.com/en/api/messages#response-content
	Content []MessagesContentBlock `json:"content"`
	// Model is the model used for the response.
	// https://docs.claude.com/en/api/messages#response-model
	Model string `json:"model"`
	// StopReason is the reason for stopping the generation.
	// https://docs.claude.com/en/api/messages#response-stop-reason
	StopReason *StopReason `json:"stop_reason,omitempty"`
	// StopSequence is the stop sequence that was encountered.
	// https://docs.claude.com/en/api/messages#response-stop-sequence
	StopSequence *string `json:"stop_sequence,omitempty"`
	// Usage contains token usage information for the response.
	// https://docs.claude.com/en/api/messages#response-usage
	Usage *Usage `json:"usage,omitempty"`
}

// ConstantMessagesResponseTypeMessages is the constant type for MessagesResponse, which is always "messages".
type ConstantMessagesResponseTypeMessages string

// ConstantMessagesResponseRoleAssistant is the constant role for MessagesResponse, which is always "assistant".
type ConstantMessagesResponseRoleAssistant string

type (
	// MessagesContentBlock represents a block of content in the Anthropic Messages API response.
	// https://platform.claude.com/docs/en/api/messages#response-content
	MessagesContentBlock struct {
		Text                *TextBlock
		Tool                *ToolUseBlock
		Thinking            *ThinkingBlock
		RedactedThinking    *RedactedThinkingBlock
		ServerToolUse       *ServerToolUseBlock
		WebSearchToolResult *WebSearchToolResultBlock
	}

	// TextBlock represents a text content block in the response.
	// https://platform.claude.com/docs/en/api/messages#text_block
	TextBlock struct {
		Type      string         `json:"type"` // Always "text".
		Text      string         `json:"text"`
		Citations []TextCitation `json:"citations,omitempty"`
	}

	// ToolUseBlock represents a tool use content block in the response.
	// https://platform.claude.com/docs/en/api/messages#tool_use_block
	ToolUseBlock struct {
		Type  string         `json:"type"` // Always "tool_use".
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}

	// ThinkingBlock represents a thinking content block in the response.
	// https://platform.claude.com/docs/en/api/messages#thinking_block
	ThinkingBlock struct {
		Type      string `json:"type"` // Always "thinking".
		Thinking  string `json:"thinking"`
		Signature string `json:"signature,omitempty"`
	}

	// RedactedThinkingBlock represents a redacted thinking content block in the response.
	// https://platform.claude.com/docs/en/api/messages#redacted_thinking_block
	RedactedThinkingBlock struct {
		Type string `json:"type"` // Always "redacted_thinking".
		Data string `json:"data"`
	}

	// ServerToolUseBlock represents a server tool use content block in the response.
	// https://platform.claude.com/docs/en/api/messages#server_tool_use_block
	ServerToolUseBlock struct {
		Type  string         `json:"type"` // Always "server_tool_use".
		ID    string         `json:"id"`
		Name  string         `json:"name"` // e.g. "web_search".
		Input map[string]any `json:"input"`
	}

	// WebSearchToolResultBlock represents a web search tool result content block in the response.
	// https://platform.claude.com/docs/en/api/messages#web_search_tool_result_block
	WebSearchToolResultBlock struct {
		Type      string                     `json:"type"` // Always "web_search_tool_result".
		ToolUseID string                     `json:"tool_use_id"`
		Content   WebSearchToolResultContent `json:"content"` // Array of WebSearchResult or a WebSearchToolResultError.
	}
)

func (m *MessagesContentBlock) UnmarshalJSON(data []byte) error {
	typ := gjson.GetBytes(data, "type")
	if !typ.Exists() {
		return errors.New("missing type field in message content block")
	}
	switch typ.String() {
	case contentBlockTypeText:
		var contentBlock TextBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal text block: %w", err)
		}
		m.Text = &contentBlock
	case contentBlockTypeToolUse:
		var contentBlock ToolUseBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal tool use block: %w", err)
		}
		m.Tool = &contentBlock
	case contentBlockTypeThinking:
		var contentBlock ThinkingBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal thinking block: %w", err)
		}
		m.Thinking = &contentBlock
	case contentBlockTypeRedactedThinking:
		var contentBlock RedactedThinkingBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal redacted thinking block: %w", err)
		}
		m.RedactedThinking = &contentBlock
	case contentBlockTypeServerToolUse:
		var contentBlock ServerToolUseBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal server tool use block: %w", err)
		}
		m.ServerToolUse = &contentBlock
	case contentBlockTypeWebSearchToolResult:
		var contentBlock WebSearchToolResultBlock
		if err := json.Unmarshal(data, &contentBlock); err != nil {
			return fmt.Errorf("failed to unmarshal web search tool result block: %w", err)
		}
		m.WebSearchToolResult = &contentBlock
	default:
		// Ignore unknown types for forward compatibility.
		return nil
	}
	return nil
}

func (m *MessagesContentBlock) MarshalJSON() ([]byte, error) {
	if m.Text != nil {
		return json.Marshal(m.Text)
	}
	if m.Tool != nil {
		return json.Marshal(m.Tool)
	}
	if m.Thinking != nil {
		return json.Marshal(m.Thinking)
	}
	if m.RedactedThinking != nil {
		return json.Marshal(m.RedactedThinking)
	}
	if m.ServerToolUse != nil {
		return json.Marshal(m.ServerToolUse)
	}
	if m.WebSearchToolResult != nil {
		return json.Marshal(m.WebSearchToolResult)
	}
	return nil, fmt.Errorf("content block must have a defined type")
}

// StopReason represents the reason for stopping the generation.
// https://docs.claude.com/en/api/messages#response-stop-reason
type StopReason string

const (
	StopReasonEndTurn                    StopReason = "end_turn"
	StopReasonMaxTokens                  StopReason = "max_tokens"
	StopReasonStopSequence               StopReason = "stop_sequence"
	StopReasonToolUse                    StopReason = "tool_use"
	StopReasonPauseTurn                  StopReason = "pause_turn"
	StopReasonRefusal                    StopReason = "refusal"
	StopReasonModelContextWindowExceeded StopReason = "model_context_window_exceeded"
)

// Usage represents token usage information for the Anthropic Messages API response.
// https://docs.claude.com/en/api/messages#response-usage
//
// NOTE: all of them are float64 in the API, although they are always integers in practice.
// However, the documentation doesn't explicitly state that they are integers in its format,
// so we use float64 to be able to unmarshal both 1234 and 1234.0 without errors.
type Usage struct {
	// The number of input tokens used to create the cache entry.
	CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
	// The number of input tokens read from the cache.
	CacheReadInputTokens float64 `json:"cache_read_input_tokens"`
	// The number of input tokens which were used.
	InputTokens float64 `json:"input_tokens"`
	// The number of output tokens which were used.
	OutputTokens float64 `json:"output_tokens"`
}

// MessagesStreamChunk represents a single event in the streaming response from the Anthropic Messages API.
// https://docs.claude.com/en/docs/build-with-claude/streaming
type MessagesStreamChunk struct {
	Type MessagesStreamChunkType
	// MessageStart is present if the event type is "message_start" or "message_delta".
	MessageStart *MessagesStreamChunkMessageStart
	// MessageDelta is present if the event type is "message_delta".
	MessageDelta *MessagesStreamChunkMessageDelta
	// MessageStop is present if the event type is "message_stop".
	MessageStop *MessagesStreamChunkMessageStop
	// ContentBlockStart is present if the event type is "content_block_start".
	ContentBlockStart *MessagesStreamChunkContentBlockStart
	// ContentBlockDelta is present if the event type is "content_block_delta".
	ContentBlockDelta *MessagesStreamChunkContentBlockDelta
	// ContentBlockStop is present if the event type is "content_block_stop".
	ContentBlockStop *MessagesStreamChunkContentBlockStop
}

// MessagesStreamChunkType represents the type of a streaming event in the Anthropic Messages API.
// https://docs.claude.com/en/docs/build-with-claude/streaming#event-types
type MessagesStreamChunkType string

const (
	MessagesStreamChunkTypeMessageStart      MessagesStreamChunkType = "message_start"
	MessagesStreamChunkTypeMessageDelta      MessagesStreamChunkType = "message_delta"
	MessagesStreamChunkTypeMessageStop       MessagesStreamChunkType = "message_stop"
	MessagesStreamChunkTypeContentBlockStart MessagesStreamChunkType = "content_block_start"
	MessagesStreamChunkTypeContentBlockDelta MessagesStreamChunkType = "content_block_delta"
	MessagesStreamChunkTypeContentBlockStop  MessagesStreamChunkType = "content_block_stop"
)

type (
	// MessagesStreamChunkMessageStart represents the message content in a "message_start".
	MessagesStreamChunkMessageStart MessagesResponse
	// MessagesStreamChunkMessageStop represents the message content in a "message_stop".
	MessagesStreamChunkMessageStop struct {
		Type MessagesStreamChunkType `json:"type"` // Type is always "message_stop".
	}
	// MessagesStreamChunkContentBlockStart represents the content block in a "content_block_start".
	MessagesStreamChunkContentBlockStart struct {
		Type         MessagesStreamChunkType `json:"type"` // Type is always "content_block_start".
		Index        int                     `json:"index"`
		ContentBlock MessagesContentBlock    `json:"content_block"`
	}
	// MessagesStreamChunkContentBlockDelta represents the content block delta in a "content_block_delta".
	MessagesStreamChunkContentBlockDelta struct {
		Type  MessagesStreamChunkType `json:"type"` // Type is always "content_block_delta".
		Index int                     `json:"index"`
		Delta ContentBlockDelta       `json:"delta"`
	}
	// MessagesStreamChunkContentBlockStop represents the content block in a "content_block_stop".
	MessagesStreamChunkContentBlockStop struct {
		Type  MessagesStreamChunkType `json:"type"` // Type is always "content_block_stop".
		Index int                     `json:"index"`
	}
	// MessagesStreamChunkMessageDelta represents the message content in a "message_delta".
	//
	// Note: the definition of this event is vague in the Anthropic documentation.
	// This follows the same code from their official SDK.
	// https://github.com/anthropics/anthropic-sdk-go/blob/3a0275d6034e4eda9fbc8366d8a5d8b3a462b4cc/message.go#L2424-L2451
	MessagesStreamChunkMessageDelta struct {
		Type MessagesStreamChunkType `json:"type"` // Type is always "message_delta".
		// Delta contains the delta information for the message.
		// This is cumulative per documentation.
		Usage Usage                                `json:"usage"`
		Delta MessagesStreamChunkMessageDeltaDelta `json:"delta"`
	}
)

type MessagesStreamChunkMessageDeltaDelta struct {
	StopReason   StopReason `json:"stop_reason"`
	StopSequence string     `json:"stop_sequence"`
}

type ContentBlockDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

func (m *MessagesStreamChunk) UnmarshalJSON(data []byte) error {
	eventType := gjson.GetBytes(data, "type")
	if !eventType.Exists() {
		return fmt.Errorf("missing type field in stream event")
	}
	m.Type = MessagesStreamChunkType(eventType.String())
	switch typ := MessagesStreamChunkType(eventType.String()); typ {
	case MessagesStreamChunkTypeMessageStart:
		messageBytes := gjson.GetBytes(data, "message")
		r := strings.NewReader(messageBytes.Raw)
		decoder := json.NewDecoder(r)
		var message MessagesStreamChunkMessageStart
		if err := decoder.Decode(&message); err != nil {
			return fmt.Errorf("failed to unmarshal message in stream event: %w", err)
		}
		m.MessageStart = &message
	case MessagesStreamChunkTypeMessageDelta:
		var messageDelta MessagesStreamChunkMessageDelta
		if err := json.Unmarshal(data, &messageDelta); err != nil {
			return fmt.Errorf("failed to unmarshal message delta in stream event: %w", err)
		}
		m.MessageDelta = &messageDelta
	case MessagesStreamChunkTypeMessageStop:
		var messageStop MessagesStreamChunkMessageStop
		if err := json.Unmarshal(data, &messageStop); err != nil {
			return fmt.Errorf("failed to unmarshal message stop in stream event: %w", err)
		}
		m.MessageStop = &messageStop
	case MessagesStreamChunkTypeContentBlockStart:
		var contentBlockStart MessagesStreamChunkContentBlockStart
		if err := json.Unmarshal(data, &contentBlockStart); err != nil {
			return fmt.Errorf("failed to unmarshal content block start in stream event: %w", err)
		}
		m.ContentBlockStart = &contentBlockStart
	case MessagesStreamChunkTypeContentBlockDelta:
		var contentBlockDelta MessagesStreamChunkContentBlockDelta
		if err := json.Unmarshal(data, &contentBlockDelta); err != nil {
			return fmt.Errorf("failed to unmarshal content block delta in stream event: %w", err)
		}
		m.ContentBlockDelta = &contentBlockDelta
	case MessagesStreamChunkTypeContentBlockStop:
		var contentBlockStop MessagesStreamChunkContentBlockStop
		if err := json.Unmarshal(data, &contentBlockStop); err != nil {
			return fmt.Errorf("failed to unmarshal content block stop in stream event: %w", err)
		}
		m.ContentBlockStop = &contentBlockStop
	default:
		return fmt.Errorf("unknown stream event type: %s", typ)
	}
	return nil
}

// ModelList represents the response from the Anthropic "/v1/models" endpoint.
// https://platform.claude.com/docs/en/api/models-list
type ModelList struct {
	Data []Model `json:"data"`
	// FirstID is the ID of the first model in the list, used for pagination.
	FirstID string `json:"first_id,omitempty"`
	// HasMore indicates whether there are more models available beyond this list.
	HasMore bool `json:"has_more"`
	// LastID is the ID of the last model in the list, used for pagination.
	LastID string `json:"last_id,omitempty"`
}

// Model represents a single model in the Anthropic "/v1/models" endpoint response.
// https://platform.claude.com/docs/en/api/models-list
type Model struct {
	// ID is the model identifier, which can be referenced in the API endpoints.
	ID string `json:"id"`
	// Type is always "model".
	Type string `json:"type"`
	// DisplayName is a human-readable name for the model.
	DisplayName string `json:"display_name"`
	// CreatedAt is the RFC 3339 datetime string representing when the model was released.
	CreatedAt string `json:"created_at"`
}

// CountTokensRequest represents a request to the Anthropic Count Tokens API.
// It is a subset of [MessagesRequest]: count_tokens accepts the same messages,
// system, tools, tool_choice, and thinking inputs, but omits output-only
// parameters such as max_tokens, temperature, stream, and stop_sequences (and,
// unlike Messages, does not require max_tokens). The nested leaf types are
// reused from [MessagesRequest] so they stay in sync.
// https://platform.claude.com/docs/en/api/messages/count_tokens
type CountTokensRequest struct {
	// Model is the model to use for token counting.
	Model string `json:"model"`

	// Messages is the list of messages in the conversation.
	Messages []MessageParam `json:"messages"`

	// System is the system prompt to guide the model's behavior.
	System *SystemPrompt `json:"system,omitempty"`

	// Thinking is the configuration for the model's "thinking" behavior.
	Thinking *Thinking `json:"thinking,omitempty"`

	// ToolChoice indicates the tool choice for the model.
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`

	// Tools is the list of tools available to the model.
	Tools []ToolUnion `json:"tools,omitempty"`
}

// CountTokensResponse represents a response from the Anthropic Count Tokens API.
// https://platform.claude.com/docs/en/api/messages/count_tokens
type CountTokensResponse struct {
	InputTokens int64 `json:"input_tokens"`
}

// ErrorResponse represents an error response from the Anthropic API.
// https://platform.claude.com/docs/en/api/errors
type ErrorResponse struct {
	Error     ErrorResponseMessage `json:"error"`
	RequestID string               `json:"request_id"`
	// Type is always "error".
	Type string `json:"type"`
}

// ErrorResponseMessage represents the error message in an Anthropic API error response
// which corresponds to the HTTP status code.
// https://platform.claude.com/docs/en/api/errors#http-errors
type ErrorResponseMessage struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}
