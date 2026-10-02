-- A next_slot post that needs approval takes its slot when approved
-- (ADR 0022). Until then it has no publish_at, and no publish_by unless the
-- caller gave one; its held targets have no times either.
ALTER TABLE posts ALTER COLUMN publish_at DROP NOT NULL;
ALTER TABLE posts ALTER COLUMN publish_by DROP NOT NULL;
ALTER TABLE post_targets ALTER COLUMN next_attempt_at DROP NOT NULL;
ALTER TABLE post_targets ALTER COLUMN publish_by DROP NOT NULL;

-- Release the slots pending posts hold. A publish_by other than the
-- default window was the caller's, and is kept.
UPDATE post_targets t SET next_attempt_at = NULL, updated_at = now(),
    publish_by = CASE WHEN p.publish_by = p.publish_at + interval '24 hours' THEN NULL ELSE p.publish_by END
FROM posts p
WHERE p.id = t.post_id AND p.status = 'pending_approval' AND p.slot_at IS NOT NULL AND t.status = 'held';
UPDATE posts SET publish_at = NULL, slot_at = NULL, updated_at = now(),
    publish_by = CASE WHEN publish_by = publish_at + interval '24 hours' THEN NULL ELSE publish_by END
WHERE status = 'pending_approval' AND slot_at IS NOT NULL;

ALTER TABLE posts ADD CONSTRAINT posts_publish_at_check
    CHECK (publish_at IS NOT NULL OR (slot_at IS NULL AND status IN ('pending_approval', 'canceled', 'rejected', 'failed')));
ALTER TABLE post_targets ADD CONSTRAINT post_targets_next_attempt_at_check
    CHECK (next_attempt_at IS NOT NULL OR status NOT IN ('queued', 'publishing'));
