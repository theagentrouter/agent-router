// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2emcp

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAIGWRun_NoConfigNoTerminal verifies that "aigw run" without any configuration fails
// with a helpful message when there is no terminal to run the interactive setup in.
func TestAIGWRun_NoConfigNoTerminal(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), aigwBin, "run") // stdin is /dev/null.
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=", "AZURE_OPENAI_API_KEY=", "ANTHROPIC_API_KEY=",
		"AIGW_CONFIG_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "to set up the providers interactively")
}
