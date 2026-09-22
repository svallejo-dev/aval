## ADDED Requirements

### Requirement: ORD-F02 Refund capped at order total
The system SHALL reject any refund whose cumulative amount would exceed the order total.

#### Scenario: Refund above remaining amount
- **WHEN** a refund request would bring the refunded total above the order total
- **THEN** the system rejects the request with a limit error and issues no refund

## MODIFIED Requirements

### Requirement: ORD-F01 Refund is idempotent
The system SHALL process a refund request at most once per idempotency key within a 24-hour window.

#### Scenario: Duplicate request with same key
- **WHEN** a refund request arrives with an idempotency key already processed
- **THEN** the system returns the original refund result without issuing a new refund

#### Scenario: Different keys for same order
- **WHEN** two refund requests arrive for the same order with different keys
- **THEN** each request is evaluated independently

#### Scenario: Key reused after the window
- **WHEN** a refund request reuses an idempotency key older than 24 hours
- **THEN** the system treats it as a new request

### Requirement: ORD-I01 Ledger entry per refund
**aval**: characterization
The system SHALL write exactly one ledger entry, with a negative amount, for every refund it issues.

#### Scenario: Refund issued
- **WHEN** a refund is issued
- **THEN** exactly one ledger entry with the refund amount is written

## REMOVED Requirements

### Requirement: ORD-A01 Legacy refund email
**Reason**: Refund notifications move to the notifications service.
**Migration**: Subscribe to the refund.issued event instead.

## RENAMED Requirements

- FROM: `### Requirement: ORD-N01 Old title`
- TO: `### Requirement: ORD-N01 New title`
