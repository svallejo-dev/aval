## ADDED Requirements

### Requirement: ORD-N01 Refund never exceeds order total
The system MUST NOT issue a refund whose cumulative amount would exceed the order total.

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

### Requirement: ORD-A01 Gateway confirms refunds at once
**Reason**: The gateway now confirms refunds asynchronously through a callback, so the assumption no longer holds.
**Migration**: Mark refunds completed only when the gateway callback arrives.

## RENAMED Requirements

- FROM: `### Requirement: ORD-S01 Fast refund answer`
- TO: `### Requirement: ORD-S01 Refund latency under 300 ms`
