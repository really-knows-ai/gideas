# Foundry Flow: Webhook Integration

**Status:** v1 Specification

## 1. Overview

Foundry Flow can notify external systems of workitem status changes using webhooks. This is useful for integrating Foundry Flow into a larger business process.

## 2. Configuration

Webhooks are configured in the `FoundryFlow` CRD:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: default
spec:
  webhooks:
    - name: on-completion
      url: "https://example.com/webhook"
      event: "workitem.completed"
    - name: on-failure
      url: "https://example.com/webhook"
      event: "workitem.failed"
```

-   `name`: A unique name for the webhook.
-   `url`: The URL to send the webhook to.
-   `event`: The event that triggers the webhook. Can be `workitem.completed` or `workitem.failed`.

## 3. Payload

The webhook payload is a JSON object with the following structure:

```json
{
  "event": "workitem.completed",
  "workitem": {
    "id": "petition-dark-mode-v1",
    "spec": {
      "type": "petition-v1",
      "intent": "Add dark mode to the dashboard"
    },
    "status": {
      "state": "Completed"
    }
  }
}
```
