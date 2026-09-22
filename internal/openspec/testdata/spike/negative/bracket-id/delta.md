## ADDED Requirements

### Requirement: [ORD-F03] Refund reason required
The system SHALL reject a refund request that carries no reason code.

#### Scenario: Missing reason code
- **WHEN** a refund request has no reason code
- **THEN** the system rejects it with a validation error
