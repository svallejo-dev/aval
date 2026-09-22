## MODIFIED Requirements

### Requirement: ORD-N01 Refund never exceeds order total ##
The system MUST NOT issue a refund whose cumulative amount would exceed the order total, pending refunds included.

#### Scenario: Refund above remaining amount
- **WHEN** a refund request would bring the refunded total above the order total
- **THEN** the system rejects the request with a limit error and issues no refund
