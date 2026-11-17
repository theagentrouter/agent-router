// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testmcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	DummyResource = &mcp.Resource{
		Name:     "dummy-resource",
		MIMEType: "text/plain",
		URI:      "file:///dummy.txt",
	}

	AnotherDummyResource = &mcp.Resource{
		Name:     "another-dummy-resource",
		MIMEType: "text/plain",
		URI:      "file:///another-dummy.txt",
	}

	// UIRendererResource is a UI resource as defined by the MCP Apps extension,
	// which requires the ui:// scheme to be preserved end-to-end.
	UIRendererResource = &mcp.Resource{
		Name:     "ui-renderer",
		MIMEType: "text/html",
		URI:      "ui://prefab/tool/renderer.html",
	}

	DummyResourceTemplate = &mcp.ResourceTemplate{
		Name:        "dummy-template",
		Description: "A dummy resource template for testing",
		MIMEType:    "text/plain",
		Title:       "Dummy Template",
		URITemplate: "file:///{name}.txt",
	}
)

func DummyResourceHandler() mcp.ResourceHandler {
	return func(_ context.Context, req *mcp.ReadResourceRequest) (_ *mcp.ReadResourceResult, err error) {
		switch req.Params.URI {
		case DummyResource.URI:
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{
					URI:  req.Params.URI,
					Blob: []byte("dummy"),
				},
			}}, nil
		case AnotherDummyResource.URI:
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{
					URI:  req.Params.URI,
					Blob: []byte("another-dummy"),
				},
			}}, nil
		case UIRendererResource.URI:
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{
					URI:      req.Params.URI,
					MIMEType: UIRendererResource.MIMEType,
					Blob:     []byte("<html>renderer</html>"),
				},
			}}, nil
		}

		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
}
