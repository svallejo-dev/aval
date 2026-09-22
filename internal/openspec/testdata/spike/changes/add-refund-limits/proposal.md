## Why
Refunds can currently exceed the order total and the idempotency window is unbounded, which lets retries issue duplicate money movements weeks later.

## What Changes
- Add ORD-F02: cap cumulative refunds at the order total.
- Modify ORD-F01: bound the idempotency window to 24 hours and add an expiry scenario.
- Modify ORD-I01: keep the characterization marker while clarifying the ledger amount sign.
- Rename ORD-N01 (same ID, clearer title).
- Remove ORD-A01: the legacy plain-text refund email moves to the notifications service.

## Impact
- Affected specs: refunds
