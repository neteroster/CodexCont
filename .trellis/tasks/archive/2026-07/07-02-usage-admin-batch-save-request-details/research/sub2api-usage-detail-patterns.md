# Sub2API Usage Detail Patterns

## Files Inspected

- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/frontend/src/views/KeyUsageView.vue`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/frontend/src/api/usage.ts`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/frontend/src/types/index.ts`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/frontend/src/utils/usagePricing.ts`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/frontend/src/utils/usageServiceTier.ts`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/backend/internal/pkg/usagestats/usage_log_types.go`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/backend/internal/service/billing_service.go`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/backend/internal/service/usage_log.go`
- `D:/Dev/20_Software/23_Reference/llm-gateway/sub2api/backend/internal/handler/dto/mappers.go`

## Useful Concepts To Borrow

Sub2API's useful design is not a specific table layout. The useful part is the
data model:

- request identity: request id, model, requested/upstream model, endpoint
- performance: duration, first token latency, stream/request type
- reasoning metadata: reasoning effort
- token categories: input, output, cache creation, cache read, total
- cost categories: input cost, output cost, cache creation cost, cache read
  cost, total cost, actual cost
- billing context: service tier, rate multiplier, billing mode/type

The user-facing DTO intentionally hides admin-only fields such as account rate
multiplier and account details. That matches our portal's security model:
show useful cost/token composition, but do not expose internal secrets or raw
payloads.

## Cost Logic Shape

Sub2API's backend `CostBreakdown` keeps separate fields:

- `InputCost`
- `OutputCost`
- `ImageOutputCost`
- `CacheCreationCost`
- `CacheReadCost`
- `TotalCost`
- `ActualCost`
- `BillingMode`

Its token calculation applies service-tier / long-context / cache multipliers
before summing total cost. For our portal, the closest safe adaptation is:

- use Key Policy prices already available to the portal
- calculate itemized costs server-side
- expose the breakdown as an additive safe projection on each event
- keep the main table's cost equal to the breakdown total

## Differences In Our Portal

Our portal receives CPAMP monitoring events, not Sub2API's native usage log
schema. Therefore:

- we can only show fields CPAMP records and Key Policy prices can price
- we should not invent `actual_cost` semantics unless we add a rate multiplier
  model later
- we should label the cost as an estimate based on Key Policy prices
- we should not expose raw CPAMP event JSON

## Recommended MVP

Implement itemized token/cost breakdown from CPAMP safe fields plus Key Policy
prices. Defer cross-service CodexCont protection correlation unless the user
explicitly wants that extra scope in this task.

