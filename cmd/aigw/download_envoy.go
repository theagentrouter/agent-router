// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"io"
	"os"

	func_e "github.com/tetratelabs/func-e"
	func_e_api "github.com/tetratelabs/func-e/api"
)

// This matches the version in the .envoy-version file at the repo root. That is ensured in tests.
// The reason why we don't use the constant defined in the EG as a library is that when we depend
// on the main branch, it will be a non-released development version that func-e may not have.
const envoyVersion = "1.39.2"

// downloadEnvoy downloads the Envoy binary used by Envoy Gateway.
func downloadEnvoy(ctx context.Context, funcERun func_e_api.RunFunc, tmpDir, dataHome string, stdout, stderr io.Writer) error {
	return funcERun(ctx, []string{"--version"},
		func_e_api.ConfigHome(tmpDir),
		func_e_api.DataHome(dataHome),
		func_e_api.StateHome(tmpDir),
		func_e_api.RuntimeDir(tmpDir),
		func_e_api.RunID("0"),
		func_e_api.EnvoyVersion(envoyVersion),
		func_e_api.Out(stdout),
		func_e_api.EnvoyOut(stdout),
		func_e_api.EnvoyErr(stderr),
	)
}

func downloadEnvoyCmd(ctx context.Context, c *cmdDownloadEnvoy, stdout, stderr io.Writer) error {
	return downloadEnvoy(ctx, func_e.Run, os.TempDir(), c.dataHome, stdout, stderr)
}
