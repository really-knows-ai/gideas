# Cross-Flow Collaboration: Foundry Bundle Format

## 1. Overview

The **Foundry Bundle** (`.fb`) is a standard serialized representation of a Workitem and its artefacts for cross-flow transport.

**Format:** `tar.gz` archive

**Purpose:**
- Encapsulate complete Workitem state for transport
- Preserve provenance for audit trails
- Enable cryptographic verification of origin

## 2. Bundle Structure

```
foundry-bundle.fb (tar.gz)
├── manifest.json           # Bundle metadata and workitem spec
├── artefacts/              # Raw artefact content
│   ├── petition-draft/
│   │   └── petition_draft.md
│   └── audit-log/
│       └── audit.json
├── provenance/
│   └── foreign_stamps.json # Original stamps (audit only)
└── signature.sig           # Export Node signature
```

## 3. Manifest Schema

```json
{
  "bundleVersion": "1",
  "sourceFlow": "flow-ideate",
  "sourceWorkitemId": "petition-dark-mode-v1",
  "exportedAt": "2026-01-08T14:30:00Z",
  "exportNode": "export-node-pod-1",
  "exportNodeCertFingerprint": "sha256:abc123...",
  
  "workitemSpec": {
    "type": "petition-v1",
    "intent": "Add dark mode to the dashboard",
    "priority": "medium",
    "requestedBy": "user@example.com"
  },
  
  "artefacts": [
    {
      "kind": "petition-draft",
      "name": "petition_draft.md",
      "hash": "sha256:def456...",
      "path": "artefacts/petition-draft/petition_draft.md"
    },
    {
      "kind": "audit-log",
      "name": "audit.json",
      "hash": "sha256:ghi789...",
      "path": "artefacts/audit-log/audit.json"
    }
  ]
}
```

## 4. Manifest Fields

| Field | Type | Description |
|-------|------|-------------|
| `bundleVersion` | string | Format version (currently `"1"`) |
| `sourceFlow` | string | Originating Flow identifier |
| `sourceWorkitemId` | string | Original Workitem name in source Flow |
| `exportedAt` | string | ISO8601 timestamp of export |
| `exportNode` | string | Name of the Export Node that created the bundle |
| `exportNodeCertFingerprint` | string | SHA256 fingerprint of signing certificate |
| `workitemSpec` | object | Original `Workitem.spec` fields |
| `artefacts` | []object | Manifest of included artefacts |

## 5. Provenance File

The `provenance/foreign_stamps.json` file preserves the original passport stamps from the source Flow:

```json
{
  "sourceFlow": "flow-ideate",
  "sourceWorkitemId": "petition-dark-mode-v1",
  "artefacts": [
    {
      "kind": "petition-draft",
      "stamps": [
        {
          "role": "linter",
          "node": "lint-node",
          "timestamp": "2026-01-08T14:00:00Z",
          "signature": "sig_rsa_4096...",
          "certificateChain": ["..."]
        },
        {
          "role": "security-reviewer",
          "node": "security-quench",
          "timestamp": "2026-01-08T14:15:00Z",
          "signature": "sig_rsa_4096...",
          "certificateChain": ["..."]
        }
      ]
    }
  ]
}
```

**Important:** These stamps are preserved for **audit purposes only**. They do NOT count toward validity in the receiving Flow. See Import documentation for naturalization details.

## 6. Bundle Signature

The `signature.sig` file contains the Export Node's cryptographic signature over the manifest:

```
SIGNATURE_ALGORITHM: RSA-SHA256
SIGNED_CONTENT: SHA256(manifest.json)
SIGNATURE: base64-encoded-signature
CERTIFICATE: base64-encoded-node-certificate
```

**Verification Chain:**
```
Treaty CA (Receiver's config)
  └─ Export Node Certificate (in signature.sig)
       └─ Signature (over manifest hash)
```
