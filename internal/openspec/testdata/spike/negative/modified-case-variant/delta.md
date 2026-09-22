## MODIFIED Requirements

### Requirement: ORD-F01 refund is idempotent
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
