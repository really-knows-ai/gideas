# Foundry Node: API Rate Limiting

**Status:** v1 Specification

## 1. Overview

Foundry Flow provides a global rate-limiting mechanism to protect the system from being overwhelmed by too many requests. This is essential for maintaining system stability and preventing accidental overload scenarios.

## 2. Design Principle: Machine Identity Only

Foundry Flow operates exclusively on **machine identity** (ServiceAccounts, mTLS certificates). User-facing applications (such as a Dashboard) are responsible for human identity, authentication, and authorization.

Rate limiting is applied **globally** at the Flow level.

## 3. Rate Limiting Strategy

Rate limiting is implemented at the gRPC level using a token bucket algorithm. Each incoming RPC consumes a token from the bucket. If the bucket is empty, the request is rejected with a `RESOURCE_EXHAUSTED` error.

## 4. Configuration

Rate limiting is configured in the `FoundryFlow` CRD:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: default
spec:
  rateLimiting:
    enabled: true
    requestsPerSecond: 100
    burst: 200
```

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | boolean | Whether rate limiting is enabled. |
| `requestsPerSecond` | integer | The number of tokens added to the bucket per second. |
| `burst` | integer | The maximum number of tokens the bucket can hold. This allows for short bursts of traffic above the steady-state rate. |

## 5. Error Handling

When a request is rate-limited, the gRPC call returns:

| gRPC Code | Foundry Reason | Description |
|-----------|----------------|-------------|
| `RESOURCE_EXHAUSTED` | `RATE_LIMITED` | The request was rejected because the rate limit was exceeded. |

Clients should implement exponential backoff and retry logic when receiving this error.
