## MODIFIED Requirements

### Requirement: ORD-F01 Refund is idempotent
The system SHALL process a refund request at most once per idempotency key within a 24-hour window.

#### Scenario: Duplicate request with same key
- **WHEN** a refund request arrives with an idempotency key already processed
- **THEN** the system returns the original refund result without issuing a new refund
