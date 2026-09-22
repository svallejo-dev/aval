## ADDED Requirements

### Requirement: ORD-F08 Refund export uses spec headers
The system SHALL export each refund policy as a markdown section like the example below.

```markdown
### Requirement: ORD-F99 Example inside a fence
```

#### Scenario: Policy exported
- **WHEN** the refund policy is exported
- **THEN** each policy becomes a markdown section
