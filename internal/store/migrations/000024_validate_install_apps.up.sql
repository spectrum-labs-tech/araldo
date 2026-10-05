SET LOCAL lock_timeout = '5s';

-- Check the rows written before 000023 (none can break these), letting
-- writes go on meanwhile (ADR 0029).
ALTER TABLE channels VALIDATE CONSTRAINT channels_one_app_check;
ALTER TABLE ad_accounts VALIDATE CONSTRAINT ad_accounts_one_app_check;
ALTER TABLE oauth_states VALIDATE CONSTRAINT oauth_states_one_app_check;
