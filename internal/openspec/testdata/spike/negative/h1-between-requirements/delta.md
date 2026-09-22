## ADDED Requirements

### Requirement: ORD-F11 Refund confirmation email
The system SHALL email a confirmation for every refund it issues.

#### Scenario: Refund issued
- **WHEN** a refund is issued
- **THEN** the customer receives a confirmation email

# Receipts

### Requirement: ORD-F12 Refund receipt number
The system SHALL give every refund a unique receipt number.

#### Scenario: Receipt assigned
- **WHEN** a refund is issued
- **THEN** the refund carries a receipt number no other refund has
