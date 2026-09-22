## MODIFIED Requirements

### Requirement: ORD-I01 Ledger entry per refund
The system SHALL write exactly one ledger entry, with a negative amount, for every refund it issues.

#### Scenario: Refund issued
- **WHEN** a refund is issued
- **THEN** exactly one ledger entry with the refund amount is written
