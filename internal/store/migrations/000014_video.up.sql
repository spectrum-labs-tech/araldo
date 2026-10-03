-- Video media (ADR 0027): what its index says, zero and empty for images.
ALTER TABLE media
    ADD COLUMN duration_ms bigint           NOT NULL DEFAULT 0,
    ADD COLUMN frame_rate  double precision NOT NULL DEFAULT 0,
    ADD COLUMN video_codec text             NOT NULL DEFAULT '',
    ADD COLUMN audio_codec text             NOT NULL DEFAULT '';
