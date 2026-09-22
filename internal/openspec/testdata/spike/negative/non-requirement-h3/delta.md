## ADDED Requirements

### Requirement: ORD-F05 Refund reason recorded
The system SHALL store the reason code of every refund it issues.

### Notes
Reason codes come from the finance catalogue.

#### Scenario: Reason stored
- **WHEN** a refund with reason code DAMAGED is issued
- **THEN** the stored refund carries reason code DAMAGED
