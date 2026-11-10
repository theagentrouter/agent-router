// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testmcp

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestTool combines a tool definition and its handler for testing.
type TestTool[In, Out any] struct {
	Tool    *mcp.Tool
	Handler mcp.ToolHandlerFor[In, Out]
}

// ptr returns a pointer to the given value. Useful to gget pointers to primitive types.
func ptr[T any](x T) *T { return &x }

// ToolEchoArgs defines the arguments for the echo tool.
type ToolEchoArgs struct {
	Text string `json:"text,omitempty"`
}

// ToolEcho - echo { text: string } -> text.
var ToolEcho = TestTool[ToolEchoArgs, any]{
	Tool: &mcp.Tool{
		Name:        "echo",
		Description: "Echo back the provided text",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"text": {Type: "string", Description: "Text to echo"},
			},
			Required: []string{"text"},
		},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolEchoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: args.Text},
			},
		}, nil, nil
	},
}

var ToolDumbEcho = TestTool[ToolEchoArgs, any]{
	Tool: &mcp.Tool{
		Name:        "dumb_echo",
		Description: "Echo back the provided text with an unnecessary prefix",
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{"text": {Type: "string", Description: "Text to echo"}},
			Required:   []string{"text"},
		},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolEchoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "dumb echo: " + args.Text}}}, nil, nil
	},
}

// ToolSumArgs defines the arguments for the sum tool.
type ToolSumArgs struct {
	A float64 `json:"a,omitempty"`
	B float64 `json:"b,omitempty"`
}

// ToolSum - sum { a: number, b: number } -> number.
var ToolSum = TestTool[ToolSumArgs, any]{
	Tool: &mcp.Tool{
		Name:        "sum",
		Description: "Return a + b",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"a": {Type: "number", Description: "First addend"},
				"b": {Type: "number", Description: "Second addend"},
			},
			Required: []string{"a", "b"},
		},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolSumArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("%g", args.A+args.B)},
			},
		}, nil, nil
	},
}

// ToolErrorArgs defines the arguments for the error tool.
type ToolErrorArgs struct {
	Error string `json:"error,omitempty"`
}

// ToolError - error { tool_error: string, mcp_error: string } -> error.
var ToolError = TestTool[ToolErrorArgs, any]{
	Tool: &mcp.Tool{
		Name:        "error",
		Description: "Return an error",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"error": {
					Type:        "string",
					Description: "Error message to return from the tool",
					MinLength:   ptr(2),
				},
			},
			Required: []string{"error"},
		},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolErrorArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{Text: args.Error},
			},
		}, nil, nil
	},
}

type ToolCountDownArgs struct {
	From     int    `json:"from,omitempty"`
	Interval string `json:"Interval,omitempty"`
}

var ToolCountDown = TestTool[ToolCountDownArgs, any]{
	Tool: &mcp.Tool{
		Name:        "countdown",
		Description: "Count down from a given number to zero",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"from": {Type: "integer", Description: "Number to count down from"},
			},
			Required: []string{"from"},
		},
	},
	Handler: func(ctx context.Context, request *mcp.CallToolRequest, args ToolCountDownArgs) (*mcp.CallToolResult, any, error) {
		interval, err := time.ParseDuration(args.Interval)
		if err != nil {
			return nil, nil, err
		}
		for i := args.From; i >= 0; i-- {
			// Send a progress notification with the current count.
			request.GetSession()
			err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				Message:       fmt.Sprintf("count down: %d", i),
				ProgressToken: args.From - i,
			})
			if err != nil {
				return nil, nil, err
			}
			// Send a log message with the debug and error levels so that
			// we can test that setting log level works.
			err = request.Session.Log(ctx, &mcp.LoggingMessageParams{
				Level: "debug",
				Data:  `debug count down: ` + fmt.Sprint(i),
			})
			if err != nil {
				return nil, nil, err
			}
			err = request.Session.Log(ctx, &mcp.LoggingMessageParams{
				Level: "error",
				Data:  `count down: ` + fmt.Sprint(i),
			})
			if err != nil {
				return nil, nil, err
			}
			time.Sleep(interval)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Done!"},
			},
		}, nil, nil
	},
}

type ToolContainsRootToolArgs struct {
	ExpectedRootName string `json:"expected_root_name,omitempty"`
}

// ToolContainsRootTool - contains_root { expected_root_name: string } -> text.
//
// This calls the server->client ListRoots method to check if a root with the given name exists.
// That will issue client->server JsonRPC response on the request path.
var ToolContainsRootTool = TestTool[ToolContainsRootToolArgs, any]{
	Tool: &mcp.Tool{
		Name:        "contains_root",
		Description: "Check if a root with the given name exists",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"expected_root_name": {Type: "string", Description: "Expression to filter root names"},
			},
			Required: []string{"expected_root_name"},
		},
	},
	Handler: func(ctx context.Context, request *mcp.CallToolRequest, args ToolContainsRootToolArgs) (*mcp.CallToolResult, any, error) {
		rs, err := request.Session.ListRoots(ctx, &mcp.ListRootsParams{})
		if err != nil {
			return nil, nil, err
		}
		has := false
		for _, r := range rs.Roots {
			if r.Name == args.ExpectedRootName {
				has = true
				break
			}
		}
		if !has {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("root %q not found", args.ExpectedRootName)},
				},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("root %q found", args.ExpectedRootName)},
			},
		}, nil, nil
	},
}

type ToolDelayArgs struct {
	Duration string `json:"duration,omitempty"`
}

// ToolDelay - delay { duration: string } -> text.
//
// This tool simply waits for the specified duration before returning.
var ToolDelay = TestTool[ToolDelayArgs, any]{
	Tool: &mcp.Tool{
		Name:        "delay",
		Description: "Delay for a given duration",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"duration": {Type: "string", Description: "Duration to delay (e.g.}, '2s', '500ms')"},
			},
			Required: []string{"duration"},
		},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolDelayArgs) (*mcp.CallToolResult, any, error) {
		d, err := time.ParseDuration(args.Duration)
		if err != nil {
			return nil, nil, err
		}
		time.Sleep(d)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Done after %s!", d)},
			},
		}, nil, nil
	},
}

// ToolAddPromptName is the name of the tool that adds a prompt dynamically which triggers the prompt handler.
const ToolAddPromptName = "add_prompt"

// ToolAddPrompt - add_prompt {} -> text.
//
// This tool adds a prompt dynamically to the server which triggers the prompt handler.
func newToolAddPrompt(s *mcp.Server) TestTool[struct{}, any] {
	return TestTool[struct{}, any]{
		Tool: &mcp.Tool{
			Name:        ToolAddPromptName,
			Description: "Delay for a given duration",
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{},
			},
		},
		Handler: func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			s.AddPrompt(DummyPrompt, nil)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "Done after %s!"}},
			}, nil, nil
		},
	}
}

const ToolResourceUpdateNotificationName = "resource_update_notification"

type ToolResourceUpdateNotificationArgs struct {
	URI string `json:"uri"`
}

// ToolResourceUpdateNotification - resource_update_notification { uri: string } -> text.
//
// This tool sends a resource update notification for the given resource URI that trigger resource list change notifications.
func newToolResourceUpdateNotification(s *mcp.Server) TestTool[ToolResourceUpdateNotificationArgs, any] {
	return TestTool[ToolResourceUpdateNotificationArgs, any]{
		Tool: &mcp.Tool{
			Name:        ToolResourceUpdateNotificationName,
			Description: "Send a resource update notification for the given resource URI",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"uri": {Type: "string", Description: "URI of the resource to update"},
				},
				Required: []string{"uri"},
			},
		},
		Handler: func(ctx context.Context, _ *mcp.CallToolRequest, args ToolResourceUpdateNotificationArgs) (*mcp.CallToolResult, any, error) {
			err := s.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: args.URI})
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Sent resource update notification for %s", args.URI)}},
			}, nil, nil
		},
	}
}

const ToolAddOrDeleteDummyResourceName = "add_or_delete_dummy_resource"

// ToolAddOrDeleteAnotherDummyResourceArgs defines the arguments for the add_or_delete_dummy_resource tool.
type ToolAddOrDeleteAnotherDummyResourceArgs struct {
	Delete bool `json:"delete,omitempty"`
}

// ToolAddOrDeleteAnotherDummyResource - add_or_delete_dummy_resource { delete: boolean } -> text.
//
// This tool adds or deletes the AnotherDummyResource resource from the server.
func newToolAddOrDeleteAnotherDummyResource(s *mcp.Server) TestTool[ToolAddOrDeleteAnotherDummyResourceArgs, any] {
	return TestTool[ToolAddOrDeleteAnotherDummyResourceArgs, any]{
		Tool: &mcp.Tool{
			Name:        ToolAddOrDeleteDummyResourceName,
			Description: "Add or delete a dummy resource",
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"delete": {Type: "boolean", Description: "Whether to delete the resource"}},
				Required:   []string{"delete"},
			},
		},
		Handler: func(_ context.Context, _ *mcp.CallToolRequest, args ToolAddOrDeleteAnotherDummyResourceArgs) (*mcp.CallToolResult, any, error) {
			if args.Delete {
				s.RemoveResources(AnotherDummyResource.URI)
			} else {
				s.AddResource(AnotherDummyResource, DummyResourceHandler())
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
		},
	}
}

// ToolElicitEmail - elicit_email {} -> text.
//
// This tool simply logs the provided email address.
var ToolElicitEmail = TestTool[struct{}, any]{
	Tool: &mcp.Tool{
		Name:        "elicit_email",
		Description: "Log the provided email address",
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{},
		},
	},
	Handler: func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		_, err := req.Session.Elicit(ctx, &mcp.ElicitParams{
			Message: "Please collect the user name and email.",
		})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "done"}},
		}, nil, nil
	},
}

// ToolCreateMessage - create_message {} -> text.
//
// This tool simply asks the model to create a message, which will trigger a CreateMessageRequest.
var ToolCreateMessage = TestTool[struct{}, any]{
	Tool: &mcp.Tool{
		Name:        "create_message",
		Description: "Create a message",
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{},
		},
	},
	Handler: func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		_, err := req.Session.CreateMessage(ctx, &mcp.CreateMessageParams{
			Messages: []*mcp.SamplingMessage{
				{
					Content: &mcp.TextContent{
						Text: "You are a coding AI assistant.",
					},
					Role: "system",
				},
				{
					Content: &mcp.TextContent{
						Text: "Build a MCP Gateway using Envoy AI Gateway.",
					},
					Role: "user",
				},
			},
			Meta: map[string]any{
				"progressToken": "sampling-foo",
			},
		})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Please create a message."},
			},
		}, nil, nil
	},
}

// ToolUIResource - returns a result carrying _meta.ui.resourceUri referencing UIRendererResource.
var ToolUIResource = TestTool[struct{}, any]{
	Tool: &mcp.Tool{
		Name:        "ui_resource",
		Description: "Return a tool result with _meta.ui.resourceUri",
		InputSchema: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Meta:    mcp.Meta{"ui": map[string]any{"resourceUri": UIRendererResource.URI}},
			Content: []mcp.Content{&mcp.TextContent{Text: "rendered"}},
		}, nil, nil
	},
}

// ToolResourceLink - returns a result carrying a ResourceLink to UIRendererResource in Content.
var ToolResourceLink = TestTool[struct{}, any]{
	Tool: &mcp.Tool{
		Name:        "resource_link",
		Description: "Return a tool result with a ResourceLink in Content",
		InputSchema: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ResourceLink{URI: UIRendererResource.URI, Name: UIRendererResource.Name}},
		}, nil, nil
	},
}

// ToolEmbeddedResource - returns a result carrying an EmbeddedResource for UIRendererResource in Content.
var ToolEmbeddedResource = TestTool[struct{}, any]{
	Tool: &mcp.Tool{
		Name:        "embedded_resource",
		Description: "Return a tool result with an EmbeddedResource in Content",
		InputSchema: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}},
	},
	Handler: func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.EmbeddedResource{
				Resource: &mcp.ResourceContents{URI: UIRendererResource.URI, MIMEType: UIRendererResource.MIMEType},
			}},
		}, nil, nil
	},
}

type notificationCounts struct {
	RootsListChanged,
	Subscribe,
	Unsubscribe atomic.Int64
}

const ToolNotificationCountsName = "notification_counts"

func newToolNotificationCounts(counts *notificationCounts) TestTool[struct{}, any] {
	return TestTool[struct{}, any]{
		Tool: &mcp.Tool{
			Name:        ToolNotificationCountsName,
			Description: "Get the counts of notification handler invocations",
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{},
			},
		},
		Handler: func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			roots := counts.RootsListChanged.Load()
			sub := counts.Subscribe.Load()
			unsub := counts.Unsubscribe.Load()
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("roots_list_changed: %d", roots)},
					&mcp.TextContent{Text: fmt.Sprintf("subscribe: %d", sub)},
					&mcp.TextContent{Text: fmt.Sprintf("unsubscribe: %d", unsub)},
				},
			}, nil, nil
		},
	}
}
