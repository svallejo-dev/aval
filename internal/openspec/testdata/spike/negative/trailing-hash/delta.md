## MODIFIED Requirements

### Requirement: ORD-F02 Refund capped at order total ##
The system SHALL reject any refund whose cumulative amount would exceed the order total, including pending refunds.

#### Scenario: Refund above remaining amount
- **WHEN** a refund request would bring the refunded total above the order total
- **THEN** the system rejects the request with a limit error and issues no refund
