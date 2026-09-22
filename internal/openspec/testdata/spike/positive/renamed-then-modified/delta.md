## MODIFIED Requirements

### Requirement: ORD-S01 Refund latency budget
The system MUST answer a refund request within 250 ms at p99 under nominal load.

#### Scenario: Nominal load latency
- **WHEN** the service receives 200 refund requests per second
- **THEN** p99 latency stays below 250 ms

## RENAMED Requirements

- FROM: `### Requirement: ORD-S01 Refund latency under 300 ms`
- TO: `### Requirement: ORD-S01 Refund latency budget`
