SET LOCAL lock_timeout = '5s';

-- Platform errors stored before v0.11 could quote a request URL with its
-- credentials: Meta's and Threads' access_token query parameter, a Discord
-- webhook's token, a Telegram bot token. They were shown to viewers, sent
-- in events and webhooks. The code no longer stores them (platform.Scrub);
-- this masks what was stored, the same way. Only matching rows are
-- touched, one row lock each.
CREATE FUNCTION pg_temp.araldo_scrub(s text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT regexp_replace(regexp_replace(regexp_replace(s,
        '\m((access|refresh|id)_token|client_secret|token|key|signature|sig|password)=[^&\s"''\x5c]+', '\1=REDACTED', 'gi'),
        '(discord(app)?\.com/api/(v[0-9]+/)?webhooks/[0-9]+/)[A-Za-z0-9_.-]+', '\1REDACTED', 'gi'),
        '/bot[0-9]+:[A-Za-z0-9_-]+', '/botREDACTED', 'g')
$$;

UPDATE post_targets SET error_message = pg_temp.araldo_scrub(error_message) WHERE error_message <> pg_temp.araldo_scrub(error_message);
UPDATE publish_attempts SET error = pg_temp.araldo_scrub(error) WHERE error <> pg_temp.araldo_scrub(error);
UPDATE channels SET status_note = pg_temp.araldo_scrub(status_note), check_error = pg_temp.araldo_scrub(check_error)
    WHERE status_note <> pg_temp.araldo_scrub(status_note) OR check_error <> pg_temp.araldo_scrub(check_error);
UPDATE webhook_deliveries SET error = pg_temp.araldo_scrub(error) WHERE error <> pg_temp.araldo_scrub(error);
UPDATE scheduled_tasks SET last_error = pg_temp.araldo_scrub(last_error) WHERE last_error <> pg_temp.araldo_scrub(last_error);
UPDATE target_engagement SET error = pg_temp.araldo_scrub(error) WHERE error <> pg_temp.araldo_scrub(error);
UPDATE ad_accounts SET status_note = pg_temp.araldo_scrub(status_note) WHERE status_note <> pg_temp.araldo_scrub(status_note);
UPDATE analytics_sources SET status_note = pg_temp.araldo_scrub(status_note) WHERE status_note <> pg_temp.araldo_scrub(status_note);
UPDATE mail_accounts SET status_note = pg_temp.araldo_scrub(status_note) WHERE status_note <> pg_temp.araldo_scrub(status_note);
UPDATE newsletter_deliveries SET last_error = pg_temp.araldo_scrub(last_error) WHERE last_error <> pg_temp.araldo_scrub(last_error);
UPDATE events SET data = pg_temp.araldo_scrub(data::text)::jsonb WHERE data::text <> pg_temp.araldo_scrub(data::text);
