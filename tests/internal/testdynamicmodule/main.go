// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package main builds the Envoy dynamic module used by the end-to-end tests. Its set_request_header filter sets one
// request header from its config, so a test backend can tell which filter ran on the requests it receives.
package main

import (
	"encoding/json"
	"fmt"

	sdk "github.com/envoyproxy/envoy/source/extensions/dynamic_modules/sdk/go"
	_ "github.com/envoyproxy/envoy/source/extensions/dynamic_modules/sdk/go/abi"
	"github.com/envoyproxy/envoy/source/extensions/dynamic_modules/sdk/go/shared"
)

func init() {
	sdk.RegisterHttpFilterConfigFactories(map[string]shared.HttpFilterConfigFactory{
		"set_request_header": &setRequestHeaderConfigFactory{},
	})
}

func main() {} // main is required to build as a C shared library.

// setRequestHeaderConfig is the filter config: the request header to set, and its value.
type setRequestHeaderConfig struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type setRequestHeaderConfigFactory struct {
	shared.EmptyHttpFilterConfigFactory
}

func (f *setRequestHeaderConfigFactory) Create(_ shared.HttpFilterConfigHandle, config []byte) (shared.HttpFilterFactory, error) {
	var c setRequestHeaderConfig
	if err := json.Unmarshal(config, &c); err != nil {
		return nil, fmt.Errorf("invalid set_request_header config: %w", err)
	}
	if c.Name == "" {
		return nil, fmt.Errorf("set_request_header config requires name")
	}
	return &setRequestHeaderFilterFactory{config: c}, nil
}

type setRequestHeaderFilterFactory struct {
	config setRequestHeaderConfig
}

func (f *setRequestHeaderFilterFactory) Create(shared.HttpFilterHandle) shared.HttpFilter {
	return &setRequestHeaderFilter{config: f.config}
}

func (f *setRequestHeaderFilterFactory) OnDestroy() {}

type setRequestHeaderFilter struct {
	shared.EmptyHttpFilter
	config setRequestHeaderConfig
}

func (f *setRequestHeaderFilter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	headers.Set(f.config.Name, f.config.Value)
	return shared.HeadersStatusContinue
}
