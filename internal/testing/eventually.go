// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RequireEventuallyNoError repeatedly calls condition until it returns nil or times out.
// The last error is included in the failure message along with msgAndArgs.
func RequireEventuallyNoError(t testing.TB, condition func() error,
	waitFor time.Duration, tick time.Duration, msgAndArgs ...any,
) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(waitFor)
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		lastErr = condition()
		if lastErr == nil {
			return
		}

		if time.Now().After(deadline) {
			msg := "Condition never satisfied"
			if len(msgAndArgs) > 0 {
				if format, ok := msgAndArgs[0].(string); ok {
					msg = fmt.Sprintf(format, msgAndArgs[1:]...)
				}
			}
			require.Fail(t, msg, "Last error: %v", lastErr)
			return
		}

		<-ticker.C
	}
}
