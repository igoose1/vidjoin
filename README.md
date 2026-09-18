# vidjoin

Joins video clips into one video at a fixed resolution. Every clip is scaled to
fit the frame, keeping its aspect ratio: big clips are shrunk and small ones
enlarged. It is then centered on a background color, so clips of a different
shape get bars, like a movie on a TV.

A single download for Linux or Windows: vidjoin plus its own copy of
ffmpeg. Nothing else needs installing. A hardware encoder (NVIDIA, Intel,
AMD) is used automatically when the machine has one.

## Usage

```
vidjoin join [options] FILE1 FILE2 ...   join files in the order given
vidjoin join [options] FOLDER            join all videos in FOLDER, sorted by name
vidjoin join [options] --list order.txt  join files in the order listed in order.txt
vidjoin order FOLDER [-o order.txt]      write an editable order.txt for FOLDER
```

| Option | Default | |
|---|---|---|
| `-o`, `--output FILE` | `joined.mp4` | `.mp4`, `.mov`, `.m4v` or `.mkv` |
| `--list FILE` | | read the order from a list file |
| `--size WxH` | `1920x1080` | also `720p`, `1080p`, `1440p`, `4k` |
| `--fps N` | `auto` | `24`, `25`, `29.97`, `30`, `50`, `60`... |
| `--bg COLOR` | `#000000` | color of the bars |
| `--preset NAME` | `quality` | `quality`, `balanced` or `fast` |
| `--dry-run` | | show the plan without encoding |
| `-y` | | overwrite the output file |

Options can be placed anywhere on the command line.

### Choosing the order

Files named on the command line are joined in exactly that order. A folder is
sorted by file name the way people expect: `clip2` comes before `clip10`.

For anything else, especially file names that are awkward to type (Chinese,
emoji, long camera names), generate a list and edit it:

```
vidjoin order "D:\Holiday"
# edit D:\Holiday\order.txt: reorder, delete or duplicate lines
vidjoin join --list "D:\Holiday\order.txt" -o holiday.mp4
```

The list is plain UTF-8 text with one file per line. Lines starting with `#`
are comments, and paths are relative to the list's folder. Run with
`--dry-run` first to check the order and how each clip will be placed.

### Frame rate

`--fps auto` picks the standard rate that covers most of your footage (e.g. 25
if most clips are 25 fps). Clips at other rates are converted.

### Presets

| Preset | x264 | NVENC | Quick Sync | AMF / VA-API | Audio |
|---|---|---|---|---|---|
| `quality` | slow, CRF 18 | p7, CQ 19 | veryslow, 20 | QP 19 | AAC 256k |
| `balanced` | medium, CRF 21 | p5, CQ 23 | medium, 23 | QP 23 | AAC 192k |
| `fast` | veryfast, CRF 24 | p2, CQ 26 | veryfast, 26 | QP 26 | AAC 160k |

Output is H.264 (High profile, 8-bit 4:2:0) with stereo 48 kHz AAC, which
plays everywhere.

## How it works

1. **Probe** every input with ffprobe: size, rotation, pixel aspect ratio,
   frame rate, duration, and whether it has audio.
2. **Detect an encoder.** vidjoin tries NVENC, then Quick Sync, then AMF
   (Windows) or VA-API (Linux) by encoding a few test frames at the output
   size, and falls back to x264 if none works.
3. **Normalize** every clip, several in parallel, to an intermediate file with
   identical parameters: `scale` (fit, both directions) → `pad` (center on
   the background) → `fps` (constant frame rate) → exact frame count.
   The audio is resampled to 48 kHz stereo and cut or padded to exactly
   the same length as the video. Clips without audio get silence.
4. **Join** the intermediates with ffmpeg's concat demuxer. The video is
   copied without re-encoding, and the audio is encoded to AAC once as a
   single continuous stream.

### Audio sync

Audio drift in joined videos comes from clips whose audio is slightly
longer or shorter than their video, which shifts every clip after it.
vidjoin prevents this in stage 3. Each clip becomes exactly *N* video
frames and exactly the corresponding number of audio samples, both
starting at zero. The intermediates keep the audio as uncompressed PCM, so
there are no per-clip AAC encoder-delay gaps, and AAC encoding happens once
over the whole joined audio. Audio that starts late in a source file is
padded with silence, and gaps in the audio are filled, so sync within each
clip is also preserved.

It was tested with 40 odd-length clips at mixed frame rates, converted to
29.97 fps: the worst audio offset anywhere in the result was 2 ms.

The clip length is its longest stream. If a source's audio runs longer
than its video, the last frame is held.

### Hardware encoding notes

- NVIDIA: needs the NVIDIA driver. Nothing else is required.
- Intel Quick Sync: needs the Intel graphics driver. On Linux, also the
  Intel media driver (`intel-media-va-driver-non-free`) and oneVPL runtime
  (`libmfx-gen1.2` or similar).
- AMD on Windows: needs the AMD driver.
- VA-API on Linux (Intel and AMD): needs the Mesa or Intel VA drivers.

Decoding and scaling run on the CPU; only encoding uses the GPU. If the
hardware encoder fails on some clip midway, vidjoin re-encodes all clips in
software, so every part is compatible for joining.

To force an encoder (e.g. for troubleshooting), set `VIDJOIN_ENCODER` to
`libx264`, `h264_nvenc`, `h264_qsv`, `h264_amf` or `h264_vaapi`.

### Disk space

Intermediate files are written to a hidden `.vidjoin-tmp-*` folder next to
the output and deleted afterwards, including on Ctrl+C. They need roughly
the size of the final video plus about 11 MB per minute of audio. Set
`VIDJOIN_KEEP_TEMP=1` to keep them for inspection.

### Limitations

- HDR input (e.g. iPhone HLG/Dolby Vision) is converted to 8-bit SDR without
  tone mapping, so it can look washed out.
- Only the first audio track of each clip is used; surround sound is mixed
  down to stereo.
- Subtitles, chapters and metadata are not carried over.

## Building

Requires Go 1.22 or newer. vidjoin has no dependencies beyond the standard
library and builds without cgo:

```
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o vidjoin ./cmd/vidjoin
GOOS=windows CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o vidjoin.exe ./cmd/vidjoin
```

To get ffmpeg and ffprobe, put them next to the executable:

```
scripts/fetch-ffmpeg.sh linux .      # or: windows .
```

vidjoin looks for ffmpeg and ffprobe next to its own executable first, then
on `PATH`.

Tests: `go test ./...`. The end-to-end test runs only when ffmpeg is found.

### CI and releases

`.github/workflows/build.yml` builds, tests and packages for Ubuntu
(`.tar.gz`) and Windows (`.zip`) on every push. Each package contains vidjoin,
ffmpeg, ffprobe and the ffmpeg license. Pushing a tag like `v1.0.0` publishes
both packages as a GitHub release. The ffmpeg build can be chosen when
running the workflow manually (default: `ffmpeg-master-latest`; pin a release
line such as `ffmpeg-n8.0-latest` for more predictability).

## ffmpeg license

The bundled ffmpeg comes from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds) (GPL variant,
needed for x264) and is licensed under the GPL. Its license is included as
`LICENSE-ffmpeg.txt`, and the corresponding source is available from that
project. vidjoin runs ffmpeg as a separate program and does not link to it.
