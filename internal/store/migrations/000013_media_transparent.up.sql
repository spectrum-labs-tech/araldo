-- Whether an image has transparent pixels, which a JPEG cannot keep: such
-- an image is never resized for a platform (ADR 0027). Images stored before
-- this were not checked; resizing checks their pixels when it decodes them.
ALTER TABLE media ADD COLUMN transparent boolean NOT NULL DEFAULT false;
