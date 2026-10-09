// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func Test_deletionOrFinalizerChangedPredicate(t *testing.T) {
	p := deletionOrFinalizerChangedPredicate{}
	oldObj := &aigv1b1.AIGatewayRoute{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", Generation: 1}}
	newObj := oldObj.DeepCopy()

	require.False(t, p.Create(event.CreateEvent{Object: newObj}))
	require.False(t, p.Delete(event.DeleteEvent{Object: newObj}))
	require.False(t, p.Generic(event.GenericEvent{Object: newObj}))
	require.False(t, p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}),
		"status-only / no metadata change must not reconcile")

	deleting := oldObj.DeepCopy()
	deleting.DeletionTimestamp = ptr.To(metav1.Now())
	require.True(t, p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: deleting}))

	finalizers := oldObj.DeepCopy()
	finalizers.Finalizers = []string{aiGatewayControllerFinalizer}
	require.True(t, p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: finalizers}))
}

func Test_handleFinalizer_addPathDoesNotAttachToDeletingObject(t *testing.T) {
	caller := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "uid-1", ResourceVersion: "1"},
	}
	server := caller.DeepCopy()
	server.DeletionTimestamp = ptr.To(metav1.Now())
	server.Finalizers = []string{aiGatewayControllerFinalizer}

	callback := 0
	mock := &mockClient{obj: caller, server: server}
	onDelete, err := handleFinalizer(context.Background(), mock, mock, caller, func(context.Context, *aigv1b1.AIGatewayRoute) error {
		callback++
		return nil
	})
	require.NoError(t, err)
	require.True(t, onDelete, "fresh Get saw deletionTimestamp; must take the delete path")
	require.Equal(t, 1, callback)
	require.Equal(t, 1, mock.updateAttempts)
	require.Empty(t, caller.Finalizers)
	require.False(t, caller.DeletionTimestamp.IsZero(), "DeepCopyInto should sync deletionTimestamp")
}

func Test_handleFinalizer_addPathNotFoundIsOnDelete(t *testing.T) {
	caller := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
	}
	mock := &mockClient{getNotFound: true, obj: caller, server: caller.DeepCopy()}
	onDelete, err := handleFinalizer(context.Background(), mock, mock, caller, nil)
	require.NoError(t, err)
	require.True(t, onDelete)
	require.Zero(t, mock.updateAttempts)
}

func Test_handleFinalizer_uidReplacementIsOnDelete(t *testing.T) {
	caller := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "old-uid"},
	}
	server := caller.DeepCopy()
	server.UID = "new-uid"
	mock := &mockClient{obj: caller, server: server}
	onDelete, err := handleFinalizer(context.Background(), mock, mock, caller, nil)
	require.NoError(t, err)
	require.True(t, onDelete)
	require.Zero(t, mock.updateAttempts)
}

func Test_handleFinalizer_deepCopyIntoSyncsMoreThanFinalizers(t *testing.T) {
	caller := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", ResourceVersion: "1"},
	}
	server := caller.DeepCopy()
	server.Labels = map[string]string{"foo": "bar"}
	server.ResourceVersion = "7"
	mock := &mockClient{obj: caller, server: server}
	onDelete, err := handleFinalizer(context.Background(), mock, mock, caller, nil)
	require.NoError(t, err)
	require.False(t, onDelete)
	require.Equal(t, "bar", caller.Labels["foo"])
	require.Contains(t, caller.Finalizers, aiGatewayControllerFinalizer)
}

type staleRVClient struct {
	client.Client
	obj            *aigv1b1.AIGatewayRoute
	staleRemaining int
	serverRV       string
	updates        int
}

func (c *staleRVClient) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	dst := obj.(*aigv1b1.AIGatewayRoute)
	c.obj.DeepCopyInto(dst)
	if c.staleRemaining > 0 {
		c.staleRemaining--
		dst.SetResourceVersion("1")
		return nil
	}
	dst.SetResourceVersion(c.serverRV)
	return nil
}

func (c *staleRVClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	c.updates++
	o := obj.(*aigv1b1.AIGatewayRoute)
	if o.GetResourceVersion() != c.serverRV {
		return apierrors.NewConflict(schema.GroupResource{
			Group: "aigateway.envoyproxy.io", Resource: "aigatewayroutes",
		}, o.Name, fmt.Errorf("stale resourceVersion"))
	}
	o.DeepCopyInto(c.obj)
	return nil
}

func Test_handleFinalizer_staleResourceVersionUntilRefresh(t *testing.T) {
	obj := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", ResourceVersion: "1"},
	}
	c := &staleRVClient{obj: obj.DeepCopy(), staleRemaining: 2, serverRV: "5"}
	onDelete, err := handleFinalizer(context.Background(), c, c, obj, nil)
	require.NoError(t, err)
	require.False(t, onDelete)
	require.GreaterOrEqual(t, c.updates, 2)
	require.Contains(t, obj.Finalizers, aiGatewayControllerFinalizer)
}
