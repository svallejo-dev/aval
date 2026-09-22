## ADDED Requirements

### Requirement: ORD-F03 Refund metrics exported
The system SHALL export a refunds_issued_total counter labelled by outcome.

#### Scenario: Refund issued
- **WHEN** a refund is issued
- **THEN** refunds_issued_total increments with outcome="issued"
