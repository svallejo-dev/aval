# refunds Specification

## Purpose
Governs how the orders service issues refunds: idempotency, latency budget, ledger integration and audit trail for every refund decision.

## Requirements

### Requirement: ORD-F01 Refund is idempotent
The system SHALL process a refund request at most once per idempotency key.

#### Scenario: Duplicate request with same key
- **WHEN** a refund request arrives with an idempotency key already processed
- **THEN** the system returns the original refund result without issuing a new refund

#### Scenario: Different keys for same order
- **WHEN** two refund requests arrive for the same order with different keys
- **THEN** each request is evaluated independently

### Requirement: ORD-N01 Old title
The system MUST answer a refund request within 300 ms at p99 under nominal load.

#### Scenario: Nominal load latency
- **WHEN** the service receives 200 refund requests per second
- **THEN** p99 latency stays below 300 ms

### Requirement: ORD-I01 Ledger entry per refund
**aval**: characterization
The system SHALL write exactly one ledger entry for every refund it issues.

#### Scenario: Refund issued
- **WHEN** a refund is issued
- **THEN** exactly one ledger entry with the refund amount is written

### Requirement: ORD-A01 Legacy refund email
The system SHALL send a plain-text email to the customer after each refund.

#### Scenario: Refund email sent
- **WHEN** a refund is issued
- **THEN** a plain-text email is sent to the customer
