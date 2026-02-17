# Governance Flow Operator: Versioning and Roadmap

## 6. Versioning & Compatibility

**API Version:** `flow.gideas.io/v1`

> **Versioning Note:** All CRDs in this specification use `flow.gideas.io/v1`. The internal version numbers (v3.6.0, v3.7.0, etc.) refer to the implementation roadmap phases, not the API version. The API version will remain `v1` until breaking changes require a `v2` release.

**Feature Flags:**
* `OPERATOR_MODE=GOVERNOR` enables Governor-specific features.

## 7. Future Enhancements (Formalised Roadmap)

* **Phase 1 - The MVP (Current):** [IMPLEMENTATION REQUIRED]
  * Canonical `Signer` interface implementation.
  * Standard **Local Provider** with automatic K8s Secret bootstrapping.
  * This is the **only phase required for v1 Foundry Flows**. All subsequent phases are forward-looking enhancements.

* **Phase 2 - The Enterprise Shift:** [DESIGN SKETCH - ROADMAP ITEM]
  * **Cloud KMS Wrappers:** Native support for Azure KeyVault (prioritised), AWS KMS, and GCP Cloud KMS.
  * Integration with Managed Identities for passwordless authority.
  * Not implemented; configuration schema documented to ensure Phase 1 design doesn't preclude this future enhancement.

* **Phase 3 - The Sovereign State:** [DESIGN SKETCH - ROADMAP ITEM]
  * **HashiCorp Vault Integration:** Support for the Transit secret engine for platform-agnostic, high-security deployments.
  * Not implemented; configuration schema documented to ensure Phase 1 design doesn't preclude this future enhancement.

**Recommendation for v1 Commitments:**
Only commit to implementing **Phase 1 (Local Provider)**. Document Phases 2 & 3 as forward-looking enhancements to guide architecture, but do not include them in v1 acceptance criteria.
