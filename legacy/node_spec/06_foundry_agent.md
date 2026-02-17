# Foundry Node: FoundryAgent Pattern

## 5.1.1 The FoundryAgent Pattern (Cognitive Task Runtime)

The SDK provides the **`FoundryAgent`** abstract base class as a managed runtime for LLM-based cognitive tasks. This pattern enforces a "Contract over Configuration" model where token accounting is an intrinsic side-effect of execution.

### Design Principles

1. **Schema-First:** Developers define strict Zod schemas for both inputs and outputs.
2. **Atomic Accounting:** Successful LLM inference automatically emits `foundry.cost.llm` telemetry.
3. **Fail-Fast Validation:** Response validation failures throw exceptions immediately.
4. **Managed Liveness:** The Agent automatically pulses the `heartbeat()` RPC in a background task during the `infer` phase to prevent SIGKILL during long reasoning loops.

> **Note:** This is the **recommended pattern for all LLM-based Nodes**. The automatic heartbeat eliminates the need to manually manage liveness during inference, regardless of the configured timeout window (see [01_security_and_health.md](./01_security_and_health.md#heartbeat-interface)). Nodes that perform LLM inference without using `FoundryAgent` must manually call `heartbeat()` at least once per timeout window.

### TypeScript Definition

```typescript
import { z } from "zod";

export abstract class FoundryAgent<TInput, TOutput> {
    abstract readonly inputSchema: z.ZodType<TInput>;
    abstract readonly outputSchema: z.ZodType<TOutput>;
    
    protected abstract infer(input: TInput): Promise<TOutput>;
    
    async execute(input: unknown): Promise<TOutput> {
        const validatedInput = this.inputSchema.parse(input);
        
        // Start background heartbeat watchdog
        const heartbeatInterval = setInterval(() => this.heartbeat(), 15000);
        
        try {
            const rawOutput = await this.inferWithBackoff(validatedInput);
            const validatedOutput = this.outputSchema.parse(rawOutput);
            
            await this.recordTelemetry({
                type: "foundry.cost.llm",
                payload: {
                    model: this.modelName,
                    tokens: this.lastTokenUsage,
                    cost_usd: this.calculateCost(this.lastTokenUsage)
                }
            });
            
            return validatedOutput;
        } finally {
            clearInterval(heartbeatInterval);
        }
    }
}
```
