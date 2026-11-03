# Copyright Envoy AI Gateway Authors

# SPDX-License-Identifier: Apache-2.0

# The full text of the Apache license is available in the LICENSE file at

# the root of the repo.

# QuotaPolicy Dynamic Metadata Limits (`fromMetadata`)

## Status

Proposed

## Summary

`QuotaPolicy` currently stores quota bucket sizes statically in
`QuotaValue.Limit` and `QuotaValue.Duration`. It already uses dynamic metadata
for the token cost (`quota_cost`) and backend/model descriptor identity, but
cannot vary a bucket size per request.

This proposal adds an optional `fromMetadata` reference to `QuotaValue`. The
reference is translated into Envoy's route-level
`envoy.config.route.v3.RateLimit.Override.DynamicMetadata` field. A preceding
HTTP filter, normally `ext_authz`, supplies a struct such as:

```json
{
  "requests_per_unit": 100000,
  "unit": "HOUR"
}
```

The static `limit` and `duration` remain mandatory. They are emitted into the
AI Gateway rate-limit service as the baseline and are used when the metadata
override is absent or invalid.

## Goals

- Support per-tenant or per-plan bucket sizes without a QuotaPolicy per tenant.
- Preserve existing static QuotaPolicy behavior and RLS configuration.
- Keep token accounting, bucket selectors, Shared mode, and shadow mode
  unchanged.
- Resolve the effective limit in Envoy's request path without a controller
  reconcile.

## Non-goals

- Dynamic values in the rate-limit service's static xDS configuration.
- Replacing `bucketRules` or their client selectors.
- Changing `QuotaPolicy` cost calculation.
- Implementing the currently incomplete `serviceQuota` data-plane path.

## Architecture

```text
Client
  |
  v
ext_authz / limit-producing filter
  | writes dynamic_metadata[namespace][key]
  | {requests_per_unit, unit}
  v
Quota rate-limit filter
  | request-time descriptor + limit override
  v
AI Gateway quota RLS <---- static baseline from QuotaPolicy
  |
  v
AI Gateway ext_proc -> LLM provider
  |
  | response usage is parsed and quota_cost is written
  v
Quota rate-limit filter, ApplyOnStreamDone
  | descriptor + HitsAddend(quota_cost)
  v
AI Gateway quota RLS
```

The limit metadata and token-cost metadata are separate:

| Purpose                | Namespace/key                                                           | Producer                                             |
| ---------------------- | ----------------------------------------------------------------------- | ---------------------------------------------------- |
| Dynamic bucket size    | Configured `fromMetadata` namespace/key                                 | `ext_authz`, `ext_proc`, Lua, or another HTTP filter |
| Token cost             | `io.envoy.ai_gateway:quota_cost`                                        | AI Gateway ext_proc                                  |
| Backend/model identity | `io.envoy.ai_gateway:ai_service_backend_name` and `model_name_override` | AI Gateway ext_proc                                  |

The metadata producer must execute before
`envoy.filters.http.ratelimit/ai-gateway-quota`. The quota filter continues to
be injected immediately before the router. Its request-time descriptors use
static GenericKey values because the ext_proc response metadata does not exist
yet; the limit override must therefore be produced on the request path.

## API design

Add the following type and field to `api/v1alpha1/quota_policy.go`:

```go
type QuotaLimitFromMetadata struct {
	// Namespace is the dynamic metadata namespace written by the producer
	// filter, for example "envoy.filters.http.ext_authz".
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Namespace string `json:"namespace"`

	// Key contains a struct with requests_per_unit and unit.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Key string `json:"key"`
}

type QuotaValue struct {
	// Static RLS baseline and fallback.
	//
	// +kubebuilder:validation:Required
	Limit uint `json:"limit"`

	// Static RLS window and fallback.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="1s";"1m";"1h";"1d";"1w";"1mo";"1y"
	Duration string `json:"duration"`

	// Optional per-request Envoy limit override.
	//
	// +optional
	FromMetadata *QuotaLimitFromMetadata `json:"fromMetadata,omitempty"`
}
```

`QuotaDefinition` and `QuotaRule` do not need new fields: every
`QuotaValue`—the service quota, default bucket, or bucket-rule quota—can
independently select a metadata source.

Static fields remain required because the quota RLS descriptor tree requires a
baseline `RateLimitPolicy`. This also makes missing metadata safely
deterministic. A policy must not use `fromMetadata` as a way to omit the RLS
descriptor.

Example:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: QuotaPolicy
metadata:
  name: provider-tenant-budget
  namespace: ai-gateway
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: provider-backend
  perModelQuotas:
    - modelName: gpt-4
      quota:
        mode: Shared
        defaultBucket:
          limit: 1000
          duration: "1h"
          fromMetadata:
            namespace: envoy.filters.http.ext_authz
            key: total_limit
        bucketRules:
          - clientSelectors:
              - headers:
                  - name: x-tenant-id
                    type: Distinct
            quota:
              limit: 100
              duration: "1h"
              fromMetadata:
                namespace: envoy.filters.http.ext_authz
                key: tenant_total_limit
```

The producer must return:

```json
{
  "dynamic_metadata": {
    "fields": {
      "total_limit": {
        "struct_value": {
          "fields": {
            "requests_per_unit": { "number_value": 50000 },
            "unit": { "string_value": "HOUR" }
          }
        }
      }
    }
  }
}
```

## xDS translation

`internal/ratelimit/translator` continues to translate static
`QuotaValue.Limit` and `Duration` into the RLS descriptor's
`RateLimitPolicy.RequestsPerUnit` and `Unit`. No dynamic value is added to the
RLS configuration.

`internal/extensionserver/quota_ratelimit.go` adds an Envoy route override:

```go
func quotaLimitOverride(qv *aigv1a1.QuotaValue) *routev3.RateLimit_Override {
	if qv == nil || qv.FromMetadata == nil {
		return nil
	}
	return &routev3.RateLimit_Override{
		OverrideSpecifier: &routev3.RateLimit_Override_DynamicMetadata_{
			DynamicMetadata: &routev3.RateLimit_Override_DynamicMetadata{
				MetadataKey: &metadatav3.MetadataKey{
					Key: qv.FromMetadata.Namespace,
					Path: []*metadatav3.MetadataKey_PathSegment{{
						Segment: &metadatav3.MetadataKey_PathSegment_Key{
							Key: qv.FromMetadata.Key,
						},
					}},
				},
			},
		},
	}
}
```

The extension server sets `RateLimit.Limit` on:

1. The request-time entry for a simple model quota.
2. Each request-time bucket-rule and default-bucket entry.
3. The matching stream-done entry that carries `HitsAddend`.

The stream-done entry uses:

```text
HitsAddend = %DYNAMIC_METADATA(io.envoy.ai_gateway:quota_cost)%
```

The request-time call checks already charged usage with the effective limit.
The stream-done call charges actual response usage with the same effective
limit. The override is not a replacement for `HitsAddend`; it controls the
bucket policy while `HitsAddend` controls how many tokens are charged.

## Execution and error semantics

The effective limit is resolved as follows:

| Input                       | Result                                            |
| --------------------------- | ------------------------------------------------- |
| No `fromMetadata`           | Static `Limit`/`Duration`                         |
| Valid metadata struct       | Per-request override                              |
| Missing metadata            | Static RLS baseline                               |
| Wrong shape or unknown unit | Override ignored; static baseline                 |
| `requests_per_unit: 0`      | Real zero limit; tenant is suspended              |
| RLS unavailable             | Existing `quotaRateLimitFailureModeDeny` behavior |

With token charging, the request that is already admitted can finish before
its response cost is charged. A zero bucket consequently denies subsequent
matching requests rather than retroactively cancelling an in-flight request.
The static fallback should be a conservative ceiling, not an unbounded
placeholder. Producers should omit the field when they have no quota opinion;
zero is reserved for suspension.

The metadata `unit` uses Envoy enum names (`SECOND`, `MINUTE`, `HOUR`, `DAY`,
`WEEK`, `MONTH`, `YEAR`). This is distinct from the CRD duration syntax
(`1s`, `1m`, `1h`, and so on). The extension server does not convert producer
values.

## Controller and indexers

No new indexer is needed. Both
`AIServiceBackendToTargetingQuotaPolicy` and `QuotaPolicyTargetNamespace`
index `targetRefs`, which are unchanged.

The QuotaPolicy controller continues publishing static RLS descriptors and
cost expressions. The extension server already reads QuotaPolicy objects while
patching generated routes and is the primary consumer of `fromMetadata`.
Existing route invalidation/spec-hash logic must include the new field so a
metadata reference change regenerates route xDS.

## Testing and rollout

### Unit tests

- `QuotaValue` with `fromMetadata` still produces the unchanged static RLS
  baseline.
- Simple model request-time and stream-done entries contain the same dynamic
  metadata override.
- Bucket-rule and default-bucket entries carry their own overrides.
- Nil `fromMetadata` preserves `Limit == nil`.
- Generated CRD contains namespace/key validation and the field.

### End-to-end test

Add a fixture based on
`tests/e2e/testdata/dynamic_metadata_ratelimit.yaml` and
`tests/e2e/testdata/backend_quota_ratelimit.yaml`:

1. An ext_authz test server emits premium/basic/suspended limit structs.
2. One QuotaPolicy points at that metadata.
3. All tiers use the same static RLS fallback.
4. Premium and basic demonstrate different effective token budgets.
5. An unknown tier demonstrates static fallback.
6. A zero tier demonstrates suspension.
7. The test verifies the quota domain, independently of Envoy Gateway's
   ordinary global rate-limit domain.

The test should also verify coexistence with a BackendTrafficPolicy using its
own `fromMetadata`; the two filters have distinct names and must not overwrite
each other's route configuration.

## Compatibility and observability

This is an additive API change. Existing QuotaPolicy objects remain valid and
produce identical RLS and route configuration. The deployed Envoy proxy must
support `RateLimit.Override.DynamicMetadata`, the same capability required by
Envoy Gateway's `BackendTrafficPolicy.limit.fromMetadata`.

No Redis schema change is required. Existing rate-limit statistics remain
available. The effective per-request limit is not represented in QuotaPolicy
status; during rollout, operators should log the producer metadata using
Envoy dynamic-metadata access-log formatters.
