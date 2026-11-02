// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// cmdValidate corresponds to `aigw validate` command.
type cmdValidate struct {
	Paths []string `arg:"" name:"paths" help:"Path(s) to AI Gateway config YAML file(s) to validate." type:"path" min:"1"`
}

// validate statically checks AI Gateway config YAML files by running them through
// the real controller pipeline against a fake Kubernetes client. This catches all
// errors that the controllers would catch at runtime without requiring a live cluster.
func validate(ctx context.Context, c *cmdValidate, stdout, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))

	yamlInput, err := readYamlsAsString(c.Paths)
	if err != nil {
		return err
	}

	aigwRoutes, mcpRoutes, aiSvcBackends, backendSecurityPolicies,
		backendTLSConfigs, referenceGrants, gws, secrets, _, err :=
		collectObjects(yamlInput, io.Discard, logger)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if err = validateCustomResourceObjects(
		ctx,
		aigwRoutes, mcpRoutes, aiSvcBackends, backendSecurityPolicies,
		backendTLSConfigs, referenceGrants, gws, secrets,
		logger,
	); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	_, _ = fmt.Fprintln(stdout, "OK: config is valid")
	return nil
}
