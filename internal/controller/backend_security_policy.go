// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
)

const (
	// aiServiceBackendGroup is the API group for AIServiceBackend resources.
	aiServiceBackendGroup = "aigateway.envoyproxy.io"
	// aiServiceBackendKind is the kind for AIServiceBackend resources.
	aiServiceBackendKind = "AIServiceBackend"
	// inferencePoolGroup is the API group for InferencePool resources.
	inferencePoolGroup = "inference.networking.k8s.io"
	// inferencePoolKind is the kind for InferencePool resources.
	inferencePoolKind = "InferencePool"
)

const (
	// clientSecretKey is key used to store Azure and OIDC client secret in Kubernetes secrets.
	clientSecretKey = "client-secret"

	// azureScopeURL specifies Microsoft Azure OAuth 2.0 scope to authenticate and authorize when accessing Azure OpenAI.
	azureScopeURL = "https://cognitiveservices.azure.com/.default"

	// preRotationWindow specifies how long before expiry to rotate credentials.
	// Temporarily a fixed duration.
	preRotationWindow = 5 * time.Minute
)

// BackendSecurityPolicyController implements [reconcile.TypedReconciler] for [aigv1b1.BackendSecurityPolicy].
//
// Exported for testing purposes.
type BackendSecurityPolicyController struct {
	client                    client.Client
	kube                      kubernetes.Interface
	logger                    logr.Logger
	aiServiceBackendEventChan chan event.GenericEvent
	inferencePoolEventChan    chan event.GenericEvent
}

func NewBackendSecurityPolicyController(client client.Client, kube kubernetes.Interface, logger logr.Logger, aiServiceBackendEventChan chan event.GenericEvent, inferencePoolEventChan chan event.GenericEvent) *BackendSecurityPolicyController {
	return &BackendSecurityPolicyController{
		client:                    client,
		kube:                      kube,
		logger:                    logger,
		aiServiceBackendEventChan: aiServiceBackendEventChan,
		inferencePoolEventChan:    inferencePoolEventChan,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for [aigv1b1.BackendSecurityPolicy].
func (c *BackendSecurityPolicyController) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	var bsp aigv1b1.BackendSecurityPolicy
	if err = c.client.Get(ctx, req.NamespacedName, &bsp); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Deleting backend security policy",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	c.logger.Info("Reconciling backend security policy", "namespace", req.Namespace, "name", req.Name)
	res, err = c.reconcile(ctx, &bsp)
	if err != nil {
		c.logger.Error(err, "failed to reconcile backend security policy", "namespace", req.Namespace, "name", req.Name)
		c.updateBackendSecurityPolicyStatus(ctx, &bsp, aigv1b1.ConditionTypeNotAccepted, err.Error())
	} else {
		c.updateBackendSecurityPolicyStatus(ctx, &bsp, aigv1b1.ConditionTypeAccepted, "BackendSecurityPolicy reconciled successfully")
	}
	return
}

// reconcile reconciles BackendSecurityPolicy but extracted from Reconcile to centralize error handling.
func (c *BackendSecurityPolicyController) reconcile(ctx context.Context, bsp *aigv1b1.BackendSecurityPolicy) (res ctrl.Result, err error) {
	if handleFinalizer(ctx, c.client, c.logger, bsp, c.syncBackendSecurityPolicy) { // Propagate the bsp deletion all the way to relevant Gateways.
		return res, nil
	}
	// Determine if credential rotation is needed
	requiresRotation := bsp.Spec.Type != aigv1b1.BackendSecurityPolicyTypeAPIKey &&
		bsp.Spec.Type != aigv1b1.BackendSecurityPolicyTypeAzureAPIKey &&
		bsp.Spec.Type != aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey

	// Skip rotation for AWS when neither credentials file nor OIDC exchange is configured
	// This allows IRSA/Pod Identity to work via the default credential chain
	if bsp.Spec.Type == aigv1b1.BackendSecurityPolicyTypeAWSCredentials {
		if bsp.Spec.AWSCredentials != nil &&
			bsp.Spec.AWSCredentials.CredentialsFile == nil &&
			bsp.Spec.AWSCredentials.OIDCExchangeToken == nil {
			c.logger.Info("Using default AWS credential chain (IRSA/Pod Identity), skipping rotation",
				"namespace", bsp.Namespace, "name", bsp.Name)
			requiresRotation = false
		}
	}

	if requiresRotation {
		res, err = c.rotateCredential(ctx, bsp)
		if err != nil {
			return res, err
		}
	}
	err = c.syncBackendSecurityPolicy(ctx, bsp)
	return res, err
}

// rotateCredential rotates the credentials using the access token from OIDC provider and return the requeue time for next rotation.
func (c *BackendSecurityPolicyController) rotateCredential(ctx context.Context, bsp *aigv1b1.BackendSecurityPolicy) (res ctrl.Result, err error) {
	var rotator rotators.Rotator

	switch bsp.Spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		oidc := getBackendSecurityPolicyAuthOIDC(&bsp.Spec)
		if oidc != nil {
			region := bsp.Spec.AWSCredentials.Region
			roleArn := bsp.Spec.AWSCredentials.OIDCExchangeToken.AwsRoleArn
			rotator, err = rotators.NewAWSOIDCRotator(ctx, c.client, nil, c.kube, c.logger, bsp.Namespace, bsp.Name, preRotationWindow, oidc, roleArn, region)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, nil
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		clientID := bsp.Spec.AzureCredentials.ClientID
		tenantID := bsp.Spec.AzureCredentials.TenantID
		var provider tokenprovider.TokenProvider
		options := policy.TokenRequestOptions{Scopes: []string{azureScopeURL}}

		if managedIdentity := bsp.Spec.AzureCredentials.ManagedIdentity; managedIdentity != nil {
			provider, err = tokenprovider.NewAzureManagedIdentityTokenProvider(ctx, managedIdentity.ClientID, options)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else if oidc := getBackendSecurityPolicyAuthOIDC(&bsp.Spec); oidc != nil {
			var oidcProvider tokenprovider.TokenProvider
			oidcProvider, err = tokenprovider.NewOidcTokenProvider(ctx, c.client, oidc)
			if err != nil {
				return ctrl.Result{}, err
			}
			provider, err = tokenprovider.NewAzureTokenProvider(ctx, tenantID, clientID, oidcProvider, options)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else if secretRef := bsp.Spec.AzureCredentials.ClientSecretRef; secretRef != nil {
			secretNamespace := bsp.Namespace
			if secretRef.Namespace != nil {
				secretNamespace = string(*secretRef.Namespace)
			}
			secretName := string(secretRef.Name)
			var secret *corev1.Secret
			secret, err = rotators.LookupSecret(ctx, c.client, secretNamespace, secretName)
			if err != nil {
				c.logger.Error(err, "failed to lookup azure client secret", "namespace", secretNamespace, "name", secretName)
				return ctrl.Result{}, err
			}
			secretValue, exists := secret.Data[clientSecretKey]
			if !exists {
				return ctrl.Result{}, fmt.Errorf("missing azure client secret key %s", clientSecretKey)
			}
			clientSecret := string(secretValue)
			provider, err = tokenprovider.NewAzureClientSecretTokenProvider(tenantID, clientID, clientSecret, options)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, fmt.Errorf("one of secret ref, oidc, or managed identity must be defined, namespace %s name %s", bsp.Namespace, bsp.Name)
		}

		rotator, err = rotators.NewAzureTokenRotator(c.client, c.kube, c.logger, bsp.Namespace, bsp.Name, preRotationWindow, provider)
		if err != nil {
			return ctrl.Result{}, err
		}
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		if err = validateGCPCredentialsParams(bsp.Spec.GCPCredentials); err != nil {
			return ctrl.Result{}, fmt.Errorf("invalid GCP credentials configuration: %w", err)
		}
		oidc := getBackendSecurityPolicyAuthOIDC(&bsp.Spec)
		if oidc != nil {
			// Create the OIDC token provider that will be used to get tokens from the OIDC provider.
			var oidcProvider tokenprovider.TokenProvider
			oidcProvider, err = tokenprovider.NewOidcTokenProvider(ctx, c.client, oidc)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to initialize OIDC provider: %w", err)
			}
			rotator, err = rotators.NewGCPOIDCTokenRotator(c.client, c.logger, bsp, preRotationWindow, oidcProvider)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else if credentialFile := bsp.Spec.GCPCredentials.CredentialsFile; credentialFile != nil {
			secretNamespace := bsp.Namespace
			if credentialFile.SecretRef.Namespace != nil {
				secretNamespace = string(*credentialFile.SecretRef.Namespace)
			}
			secretName := string(credentialFile.SecretRef.Name)
			var secret *corev1.Secret
			secret, err = rotators.LookupSecret(ctx, c.client, secretNamespace, secretName)
			if err != nil {
				c.logger.Error(err, "failed to lookup gcp service account key secret", "namespace", secretNamespace, "name", secretName)
				return ctrl.Result{}, err
			}
			serviceAccountKeyJSON, exists := secret.Data[rotators.GCPServiceAccountJSON]
			if !exists {
				return ctrl.Result{}, fmt.Errorf("missing gcp service account key %s", rotators.GCPServiceAccountJSON)
			}
			var tokenProvider tokenprovider.TokenProvider
			tokenProvider, err = tokenprovider.NewGCPTokenProvider(ctx, serviceAccountKeyJSON)
			if err != nil {
				return ctrl.Result{}, err
			}

			rotator, err = rotators.NewGCPTokenRotator(c.client, c.kube, c.logger, bsp.Namespace, bsp.Name, preRotationWindow, tokenProvider)
			if err != nil {
				return ctrl.Result{}, err
			}
		} else {
			// Use Application Default Credentials (ADC) - handled by extproc, skip rotation
			c.logger.Info("Using GCP Application Default Credentials (ADC), skipping rotation",
				"namespace", bsp.Namespace, "name", bsp.Name)
			return ctrl.Result{}, nil
		}

	default:
		err = fmt.Errorf("backend security type %s does not support OIDC token exchange", bsp.Spec.Type)
		c.logger.Error(err, "unsupported backend security type", "namespace", bsp.Namespace, "name", bsp.Name)
		return ctrl.Result{}, err
	}
	res, err = c.executeRotation(ctx, rotator, bsp)
	if err != nil {
		c.logger.Error(err, "failed to execute rotation", "namespace", bsp.Namespace, "name", bsp.Name)
		return res, fmt.Errorf("failed to execute rotation for backend security policy %s/%s: %w", bsp.Namespace, bsp.Name, err)
	}

	if secretName := getBSPGeneratedSecretName(bsp); secretName != "" {
		var secret *corev1.Secret
		secret, err = rotators.LookupSecret(ctx, c.client, bsp.Namespace, secretName)
		if err != nil {
			return res, fmt.Errorf("failed to lookup backend security policy secret %s/%s: %w",
				bsp.Namespace, secretName, err)
		}
		ok, _ := ctrlutil.HasOwnerReference(secret.OwnerReferences, bsp, c.client.Scheme())
		if !ok {
			updated := secret.DeepCopy()
			if err = ctrlutil.SetControllerReference(bsp, updated, c.client.Scheme()); err != nil {
				panic(fmt.Errorf("BUG: failed to set controller reference for secret %s/%s: %w", bsp.Namespace, bsp.Name, err))
			}
			if err = c.client.Update(ctx, updated); err != nil {
				return res, fmt.Errorf("failed to update secret %s/%s with owner reference: %w",
					updated.Namespace, updated.Name, err)
			}
		}
	}
	return res, nil
}

func (c *BackendSecurityPolicyController) executeRotation(ctx context.Context, rotator rotators.Rotator, bsp *aigv1b1.BackendSecurityPolicy) (res ctrl.Result, err error) {
	requeue := time.Minute
	var rotationTime time.Time
	rotationTime, err = rotator.GetPreRotationTime(ctx)
	if err != nil && !apierrors.IsNotFound(err) {
		c.logger.Error(err, "failed to get rotation time, retry in one minute")
	} else {
		if rotator.IsExpired(rotationTime) {
			var expirationTime time.Time
			expirationTime, err = rotator.Rotate(ctx)
			if err != nil {
				c.logger.Error(err, "failed to rotate token, retry in one minute")
			} else {
				rotationTime = expirationTime.Add(-preRotationWindow)
				if r := time.Until(rotationTime); r > 0 {
					requeue = r
					c.logger.Info(
						fmt.Sprintf("successfully rotated credential for %s in namespace %s of auth type %s, renewing in %f minutes",
							bsp.Name, bsp.Namespace, bsp.Spec.Type, requeue.Minutes()))
				} else {
					err = fmt.Errorf("newly rotated credential is already expired %s", rotationTime)
					c.logger.Error(err, err.Error(), "namespace", bsp.Namespace, "name", bsp.Name)
				}
			}
		} else {
			requeue = time.Until(rotationTime)
			c.logger.Info(fmt.Sprintf("credentials has not yet expired for %s in namespace %s of auth type %s, renewing in %f minutes",
				bsp.Name, bsp.Namespace, bsp.Spec.Type, requeue.Minutes()))
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, err
}

// getBackendSecurityPolicyAuthOIDC returns the backendSecurityPolicy's OIDC pointer or nil.
func getBackendSecurityPolicyAuthOIDC(spec *aigv1b1.BackendSecurityPolicySpec) *egv1a1.OIDC {
	switch spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		if spec.AWSCredentials != nil && spec.AWSCredentials.OIDCExchangeToken != nil {
			return &spec.AWSCredentials.OIDCExchangeToken.OIDC
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		if spec.AzureCredentials != nil && spec.AzureCredentials.OIDCExchangeToken != nil {
			return &spec.AzureCredentials.OIDCExchangeToken.OIDC
		}
		return nil
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		if spec.GCPCredentials != nil && spec.GCPCredentials.WorkloadIdentityFederationConfig != nil {
			return &spec.GCPCredentials.WorkloadIdentityFederationConfig.OIDCExchangeToken.OIDC
		}
	}
	return nil
}

// backendSecurityPolicyKey returns the key used for indexing and caching the backendSecurityPolicy.
func backendSecurityPolicyKey(namespace, name string) string {
	return fmt.Sprintf("%s.%s", name, namespace)
}

func (c *BackendSecurityPolicyController) syncBackendSecurityPolicy(ctx context.Context, bsp *aigv1b1.BackendSecurityPolicy) error {
	for _, targetRef := range bsp.Spec.TargetRefs {
		switch {
		case targetRef.Group == aiServiceBackendGroup && targetRef.Kind == aiServiceBackendKind:
			var aiBackend aigv1b1.AIServiceBackend
			err := c.client.Get(ctx, client.ObjectKey{
				Name:      string(targetRef.Name),
				Namespace: bsp.Namespace, // targetRefs are local to the policy's namespace.
			}, &aiBackend)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("failed to get targeted AIServiceBackend %s: %w", targetRef.Name, err)
				}
				c.logger.Info("Targeted AIServiceBackend not found", "name", string(targetRef.Name), "namespace", bsp.Namespace)
				continue
			}
			c.logger.Info("Syncing AIServiceBackend", "namespace", aiBackend.Namespace, "name", aiBackend.Name)
			c.aiServiceBackendEventChan <- event.GenericEvent{Object: &aiBackend}
		case targetRef.Group == inferencePoolGroup && targetRef.Kind == inferencePoolKind:
			var inferencePool gwaiev1.InferencePool
			err := c.client.Get(ctx, client.ObjectKey{
				Name:      string(targetRef.Name),
				Namespace: bsp.Namespace, // targetRefs are local to the policy's namespace.
			}, &inferencePool)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("failed to get targeted InferencePool %s: %w", targetRef.Name, err)
				}
				c.logger.Info("Targeted InferencePool not found", "name", string(targetRef.Name), "namespace", bsp.Namespace)
				continue
			}
			c.logger.Info("Syncing InferencePool", "namespace", inferencePool.Namespace, "name", inferencePool.Name)
			c.inferencePoolEventChan <- event.GenericEvent{Object: &inferencePool}
		}
	}

	return nil
}

// updateBackendSecurityPolicyStatus updates the status of the BackendSecurityPolicy.
func (c *BackendSecurityPolicyController) updateBackendSecurityPolicyStatus(ctx context.Context, bsp *aigv1b1.BackendSecurityPolicy, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: bsp.Name, Namespace: bsp.Namespace}, bsp); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		bsp.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, bsp)
	})
	if err != nil {
		c.logger.Error(err, "failed to update BackendSecurityPolicy status",
			"namespace", bsp.Namespace, "name", bsp.Name)
	}
}

func validateGCPCredentialsParams(gcpCreds *aigv1b1.BackendSecurityPolicyGCPCredentials) error {
	if gcpCreds == nil {
		return fmt.Errorf("invalid backend security policy, gcp credentials cannot be nil")
	}
	if gcpCreds.ProjectName == "" {
		return fmt.Errorf("invalid GCP credentials configuration: projectName cannot be empty")
	}
	if gcpCreds.Region == "" {
		return fmt.Errorf("invalid GCP credentials configuration: region cannot be empty")
	}

	wifConfig := gcpCreds.WorkloadIdentityFederationConfig
	if wifConfig != nil {
		if wifConfig.ProjectID == "" {
			return fmt.Errorf("invalid GCP Workload Identity Federation configuration: projectID cannot be empty")
		}
		if wifConfig.WorkloadIdentityPoolName == "" {
			return fmt.Errorf("invalid GCP Workload Identity Federation configuration: workloadIdentityPoolName cannot be empty")
		}
		if wifConfig.WorkloadIdentityProviderName == "" {
			return fmt.Errorf("invalid GCP Workload Identity Federation configuration: workloadIdentityProvider.name cannot be empty")
		}
	}
	return nil
}

// getBSPGeneratedSecretName returns a secret's name generated by the input bsp
// if there's any. This returns an empty string if there's no generated secret.
func getBSPGeneratedSecretName(bsp *aigv1b1.BackendSecurityPolicy) string {
	switch bsp.Spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		if bsp.Spec.AWSCredentials.OIDCExchangeToken == nil {
			return ""
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		if bsp.Spec.AzureCredentials.OIDCExchangeToken == nil {
			return ""
		}
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		if bsp.Spec.GCPCredentials.WorkloadIdentityFederationConfig == nil {
			return ""
		}
	case aigv1b1.BackendSecurityPolicyTypeAPIKey,
		aigv1b1.BackendSecurityPolicyTypeAzureAPIKey,
		aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:
		return "" // APIKey does not require rotation.
	default:
		panic("BUG: unsupported backend security policy type: " + string(bsp.Spec.Type))
	}
	return rotators.GetBSPSecretName(bsp.Name)
}
