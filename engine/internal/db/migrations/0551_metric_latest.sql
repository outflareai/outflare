-- Metrics gain the latest aggregate: the value of a property in the latest
-- matching event, for something a product reports as it stands (a prepaid
-- balance, a plan limit, a seat count). It needs no new column: a day's
-- bucket keeps its last value in total. The version before treats an
-- aggregate it doesn't know as last, so it still runs beside this one.
ALTER TABLE metric_definitions DROP CONSTRAINT metric_definitions_aggregate_check;
ALTER TABLE metric_definitions ADD CONSTRAINT metric_definitions_aggregate_check
    CHECK (aggregate IN ('count', 'sum', 'min', 'max', 'days', 'last', 'latest'));
