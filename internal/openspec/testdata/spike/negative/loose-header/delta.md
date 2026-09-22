## ADDED Requirements

###requirement:ORD-F06 Refund currency matches order
The system SHALL issue every refund in the currency of the original order.

#### Scenario: Order paid in EUR
- **WHEN** a refund is issued for an order paid in EUR
- **THEN** the refund amount is expressed in EUR
