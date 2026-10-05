SET LOCAL lock_timeout = '5s';

-- When an endpoint's secret is rolled, the old one keeps signing deliveries
-- beside the new one until previous_secret_until, so receivers can switch
-- without rejecting any (as Stripe does; ADR 0012).
ALTER TABLE webhook_endpoints
    ADD COLUMN previous_secret       bytea,
    ADD COLUMN previous_secret_until timestamptz;
