# Foundry Flow: Audit Logging

**Status:** v1 Specification

## 1. Overview

Foundry Flow provides a comprehensive audit logging mechanism to track critical governance and security events. This allows operators to answer the questions of "who did what, and when?"

## 2. Audit Log Architecture

Audit logs are structured JSON objects written to a dedicated, append-only log file. This file can be collected by standard log aggregation tools (e.g., Fluentd, Logstash) and shipped to a security information and event management (SIEM) system for analysis and alerting.

## 3. Audited Events

The following events are recorded in the audit log:

| Event Name | Description |
| :--- | :--- |
| `law.create` | A new law was created. |
| `law.update` | An existing law was amended. |
| `law.retire` | A law was retired. |
| `treaty.propose` | A new treaty was proposed. |
| `treaty.accept` | A treaty was accepted. |
| `treaty.reject` | A treaty was rejected. |
| `certificate.issue` | A new node certificate was issued. |
| `certificate.revoke` | A node certificate was revoked. |
| `workitem.create` | A new workitem was created. |
| `workitem.complete` | A workitem was completed. |
| `workitem.fail` | A workitem failed. |

## 4. Audit Log Format

Each audit log entry is a JSON object with the following structure:

```json
{
  "timestamp": "2026-01-10T12:00:00Z",
  "event_name": "law.create",
  "user": {
    "name": "jane.doe",
    "ip_address": "192.168.1.100"
  },
  "object": {
    "type": "law",
    "id": "f-123",
    "details": {
      "statement": "All new code must have at least 80% test coverage."
    }
  }
}
```

## 5. Retention and Access Control

-   Audit logs should be retained for a minimum of one year.
-   Access to the audit logs should be restricted to authorized security and privileged security personnel.
