# Embedding Provider Interface

> Status: Draft Implementation Contract

This document specifies the interface contract for embedding providers used by the Librarian for semantic search and conflict detection.

## 1. Production Recommendation: Local Models

The embedding provider is a pluggable component. For production workloads, **local, self-hosted models (e.g., via Ollama) are the recommended configuration** to ensure availability and low latency. Cloud-based providers like OpenAI or Azure should only be used for development and testing.

**Rationale:** A local model eliminates network dependency, external API rate limits, and data privacy concerns.

## 2. Request Schema
- Input: batch of strings (law statements) with optional labels.
- Fields: `id`, `text`, `metadata`.

## 3. Response Schema
- Output: array of embeddings with `id`, `vector[]`, `model`, `dimensions`.
- Errors: rate limit, provider unavailable, invalid input.

## 4. Error Handling
- Retry with exponential backoff on transient errors.
- Circuit-breaker when error rate exceeds threshold.

### 4.1 Circuit Breaker Configuration

```yaml
embeddingConfig:
  circuitBreaker:
    errorThreshold: 5        # Open circuit after 5 consecutive failures
    resetTimeout: "30s"      # Try again after 30s
    halfOpenRequests: 2      # Allow 2 test requests in half-open state
```

## 5. Caching
- Cache by `hash(text)`; expire via TTL.
- Warm-up cache during idle periods.

## 6. Supported Providers
- OpenAI-compatible endpoint
- Ollama local models
- Azure OpenAI

## 7. Model Requirements
- Dimensions must match Helm `embedding.dimensions`.
- Vector normalization: cosine similarity baseline.
