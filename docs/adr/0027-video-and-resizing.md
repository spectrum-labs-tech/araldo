# ADR 0027: Images too big for a platform are resized for it, and video is media stored in object storage

- Status: proposed
- Date: 2026-10-03

## Context

[ADR 0017](0017-media.md) made images media, checked against each
platform's rules, and left two things for later: resizing, and video.

Resizing: Bluesky takes images up to 1,000,000 bytes, and a photo from any
phone is larger. Refusing it, as Araldo does today, is correct and
unhelpful: the author has to shrink the file by hand for one network and
keep the original for the others. Every scheduler sold today shrinks it.

Video: short video is how most networks now rank reach, two of the largest
(YouTube and TikTok) take nothing else, and every network Araldo supports
except Pinterest's image pins takes it. A video is tens to hundreds of
megabytes: too big to hold in memory, to keep in a Postgres row, or to
send as one request without care. Several networks also process a video
after receiving it, and publish only when that is done.

## Decision

### Resizing images

1. **An image too big for a platform is resized for it** when the post is
   published: scaled down, keeping its shape, and re-encoded as JPEG at
   decreasing quality until it fits the platform's size and dimension
   limits. The original is kept and goes unchanged to every platform that
   takes it. Nothing is cropped or padded: an image the wrong shape for a
   platform (Instagram's 4:5 to 1.91:1) is still refused, since changing
   its framing is the author's decision.
2. **A preview says when an image will be resized**, per channel, as a
   notice rather than a violation. A GIF or a transparent PNG too big for a
   platform is refused rather than flattened, and so is anything still too
   big at the lowest quality Araldo will use.
3. **Pixels are decoded only within bounds**: an image over 50 megapixels
   by its header is refused before decoding, so a decompression bomb costs
   nothing. Decoding and scaling use `golang.org/x/image` (WebP, and a
   Catmull-Rom scaler), maintained by the Go team; JPEG, PNG and GIF use the
   standard library.

### Video

4. **Video is media** (`media_…`, [ADR 0017](0017-media.md)) of type
   `video/mp4` or `video/quicktime`, recognized from its bytes. Its
   duration, dimensions (after the rotation phones record), frame rate and
   codecs are read from the container's index, never by decoding a frame.
   Araldo does not transcode: a video is posted as uploaded, and a
   platform's rules (duration, size, dimensions, codecs) are checked
   against what was read, as images' are.
5. **Video lives in S3-compatible storage**, never in Postgres. An install
   without `ARALDO_S3_BUCKET` refuses video with a message saying so. The
   largest video an install accepts is `ARALDO_MAX_VIDEO_BYTES` (default
   1 GiB).
6. **Uploads stream**: through `POST /v1/media` (multipart, the file part
   read as it arrives) or from a URL, into an S3 multipart upload, so no
   process holds a whole video in memory or on disk. The dashboard's form
   uploads the same way and needs no script. Reading the container's index
   for the checks uses ranged reads.
7. **Publishing streams too**: an adapter reads the video in chunks for
   platforms that take uploads (X, Bluesky, Mastodon, LinkedIn, YouTube,
   Telegram), or gives a signed link for those that fetch it themselves
   (Instagram, Threads, Facebook, TikTok, [ADR 0021](0021-oauth-connections.md)).
8. **Platform processing is part of publishing**: where a network
   processes a video before it can be posted, the adapter waits for it,
   within a bound, and an attempt still waiting is uncertain only if
   something may have been posted ([ADR 0011](0011-publishing.md)). The
   publisher's lease is renewed while it waits.
9. **YouTube and TikTok are platforms** that take only video, each through
   the org's developer app ([ADR 0021](0021-oauth-connections.md)). A
   YouTube post is a video with a title (the text's first line) and a
   description; privacy is the channel's setting. A TikTok post goes out
   through TikTok's Content Posting API, under the limits TikTok sets for
   unaudited apps until it audits the org's app.

### Phases

- **Phase 1:** resizing images.
- **Phase 2:** video media (probe, streaming storage), its rules, and
  video on the platforms that take uploads simply (Bluesky, X, Mastodon,
  Telegram, Discord, Facebook, LinkedIn).
- **Phase 3:** YouTube; then Instagram and Threads reels; then TikTok.

## Alternatives considered

- **Cropping or padding to fit.** It would let every image through, and
  would change what the author framed. Refusing with the reason, as
  ADR 0017 does for shapes, stays.
- **Transcoding with ffmpeg.** It would make any video fit any platform,
  and would put a large native dependency, and minutes of CPU per video,
  in a single static binary. Authors export video in the common format
  (H.264 in MP4) that every platform takes.
- **Uploading straight to the bucket with presigned URLs.** It spares the
  server the bandwidth, and needs the bucket's CORS set up and scripts in
  the dashboard. Streaming through the server works everywhere first.
- **Video in Postgres.** One row can hold a gigabyte, and should not.

## Consequences

- One new module (`golang.org/x/image`).
- Media gains video fields (duration, frame rate, codecs); posts with
  video are previewed against each channel's video rules.
- The S3 client learns multipart uploads and ranged reads.
- Each adapter's media code grows a video path, tested against fake
  servers as before; adapters for platforms that process video learn to
  wait for it.
