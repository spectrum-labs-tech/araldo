ALTER TABLE media
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS frame_rate,
    DROP COLUMN IF EXISTS video_codec,
    DROP COLUMN IF EXISTS audio_codec;
