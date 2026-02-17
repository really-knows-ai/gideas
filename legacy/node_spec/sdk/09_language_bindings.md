# Foundry SDK: Language Bindings

**Status:** v1 Specification

## 1. Overview

This document specifies the officially supported programming languages for the Foundry Node SDK. The goal is to enable developers to build `FoundryNode` implementations idiomatically in the language of their choice.

## 2. Officially Supported Languages

The following languages have official SDK support:

| Language | Compatibility | Notes |
|---|---|---|
| **Go** | — | The reference implementation. Used by the Foundry Flow control plane. |
| **TypeScript** | JavaScript | Full JavaScript compatibility via compiled output. |
| **Python** | — | Essential for AI/ML workloads and data science nodes. |
| **Java** | Kotlin | Kotlin has 100% interoperability with the Java SDK. |
| **Rust** | — | For performance-critical and safety-critical nodes. |

## 3. Guiding Principles

**Idiomatic:** SDKs feel natural to developers in that language. This means using language-specific conventions for naming, error handling, and asynchronous operations (e.g., `async/await` in TypeScript, `async` in Python, `Futures` in Rust).

**Generated from Protobuf:** The core gRPC client and message types are automatically generated from the `sidecar.proto` definitions to ensure consistency and correctness.

**Thin Wrapper:** The SDK provides a thin, ergonomic wrapper around the generated gRPC client, handling session management, heartbeats, and error translation.

**Feature Parity:** All official SDKs implement the same set of functionalities.

## 4. SDK Structure

A typical SDK has the following structure:

```
foundry-sdk-{language}/
├── generated/         # Auto-generated gRPC client and protobuf messages
├── src/               # Handwritten wrapper code
│   ├── client.{ext}   # Main SDK client, handles session
│   ├── errors.{ext}   # Custom error types
│   └── ...
├── examples/          # Example node implementations
└── README.md          # Setup and usage instructions
```

## 5. Kotlin Interoperability

Kotlin has 100% interoperability with Java, meaning the Java SDK can be used directly from Kotlin code. A dedicated Kotlin wrapper may be provided in the future to offer a more idiomatic experience with coroutines and DSL-style builders.

### Example: Kotlin Node Implementation (using Java SDK)

```kotlin
class RefineNode : FoundryNodeHandler {
    override fun process(ctx: WorkitemContext): CompletableFuture<WorkitemResult> {
        val draft = ctx.fetchArtefact("petition-draft").get()
            ?: return CompletableFuture.completedFuture(ctx.routeToOutput("error"))
        
        val feedback = ctx.listFeedback(target = draft.name)
            .filter { it.state == FeedbackState.PENDING }
        
        // Process feedback...
        
        return CompletableFuture.completedFuture(ctx.routeToOutput("pass"))
    }
}
```

## 6. Enterprise Coverage

The combination of **Go, TypeScript, Python, Java, and Rust** provides coverage for the vast majority of enterprise use cases:

| Domain | Languages Covered |
|---|---|
| **Cloud-Native Infrastructure** | Go, Rust |
| **AI/ML and Data Science** | Python |
| **Web and API Development** | TypeScript, Python |
| **Enterprise Backend Systems** | Java, Go |
| **High-Performance Computing** | Rust, Go |
| **Mobile (Android)** | Java (Kotlin-compatible) |
