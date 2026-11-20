// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// captureLogger returns a logger that appends every formatted log line to the returned slice pointer.
func captureLogger(t *testing.T) (logr.Logger, func() string) {
	t.Helper()
	var mu sync.Mutex
	var sb strings.Builder
	l := funcr.New(func(prefix, args string) {
		mu.Lock()
		defer mu.Unlock()
		sb.WriteString(prefix + args + "\n")
	}, funcr.Options{})
	return l, func() string {
		mu.Lock()
		defer mu.Unlock()
		return sb.String()
	}
}

func TestExtensionServerCheck_check(t *testing.T) {
	newClient := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().WithScheme(Scheme).WithObjects(objs...).Build()
	}
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
	}
	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "mymcproute", Namespace: "default"},
	}

	t.Run("invoked", func(t *testing.T) {
		logger, logs := captureLogger(t)
		e := &extensionServerCheck{client: newClient(aiGatewayRoute), logger: logger, invoked: func() bool { return true }}
		require.True(t, e.check(t.Context()))
		require.Empty(t, logs())
	})

	t.Run("not invoked without routes", func(t *testing.T) {
		logger, logs := captureLogger(t)
		e := &extensionServerCheck{client: newClient(), logger: logger, invoked: func() bool { return false }}
		require.False(t, e.check(t.Context()))
		require.Empty(t, logs())
	})

	t.Run("not invoked with AIGatewayRoute", func(t *testing.T) {
		logger, logs := captureLogger(t)
		e := &extensionServerCheck{client: newClient(aiGatewayRoute), logger: logger, invoked: func() bool { return false }}
		require.False(t, e.check(t.Context()))
		require.Contains(t, logs(), errExtensionServerNotInvoked.Error())
		require.Contains(t, logs(), `"aigatewayroute_count"=1`)
	})

	t.Run("not invoked with MCPRoute", func(t *testing.T) {
		logger, logs := captureLogger(t)
		e := &extensionServerCheck{client: newClient(mcpRoute), logger: logger, invoked: func() bool { return false }}
		require.False(t, e.check(t.Context()))
		require.Contains(t, logs(), `"mcproute_count"=1`)
	})

	t.Run("list error", func(t *testing.T) {
		logger, logs := captureLogger(t)
		e := &extensionServerCheck{
			client:  &errorListClient{Client: newClient(), listErr: errors.New("boom")},
			logger:  logger,
			invoked: func() bool { return false },
		}
		require.False(t, e.check(t.Context()))
		require.Contains(t, logs(), "boom")
		require.NotContains(t, logs(), errExtensionServerNotInvoked.Error())
	})
}

func TestExtensionServerCheck_Start(t *testing.T) {
	t.Run("stops once invoked", func(t *testing.T) {
		var invoked bool
		e := &extensionServerCheck{
			client:   fake.NewClientBuilder().WithScheme(Scheme).Build(),
			logger:   logr.Discard(),
			interval: time.Millisecond,
			invoked:  func() bool { defer func() { invoked = true }(); return invoked },
		}
		require.NoError(t, e.Start(t.Context()))
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		e := &extensionServerCheck{
			client:   fake.NewClientBuilder().WithScheme(Scheme).Build(),
			logger:   logr.Discard(),
			interval: time.Hour,
			invoked:  func() bool { return false },
		}
		cancel()
		require.NoError(t, e.Start(ctx))
	})
}
