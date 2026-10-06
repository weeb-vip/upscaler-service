# upscaler-service

Upscales artwork with [Real-ESRGAN](https://github.com/xinntao/Real-ESRGAN).
The default model is the general `realesrgan-x4plus`: the catalogue is
promotional posters that mix illustration, painted backgrounds, logos and
typography, and the anime-trained model redraws all of it as flat colour and
clean strokes, turning small text into invented letter shapes. The general
model keeps text and gradients honest at the cost of slightly softer line
art; `UPSCALER_MODEL=realesrgan-x4plus-anime` switches back.

The service shells out to a runner with the command line of the authors'
`realesrgan-ncnn-vulkan` build. In the container that runner is
`runner/upscale.py`: the model on the CPU through ONNX Runtime, tiled so
memory is bounded (about 700 MB), a poster in seconds to tens of seconds
depending on the CPU. The ncnn build is GPU-only -- on a software Vulkan
driver it needed more than 3 GB and ten minutes per poster -- but the same
service runs it where a GPU exists (`UPSCALER_BINARY`), a Mac included.

## Endpoints

| Method | Path | What |
|---|---|---|
| `POST` | `/upscale?format=png\|jpg\|webp` | Body is the image (raw bytes, or a multipart form with an `image` field). Response is the upscaled image. |
| `GET` | `/healthz` | 200 with the model in use, 503 if the binary is missing. |

```sh
curl --data-binary @poster.jpg -H 'Content-Type: image/jpeg' \
     'http://localhost:3000/upscale?format=webp&scale=2' -o poster-2x.webp
```

`scale` is 2, 3 or 4 (the x4 models downscale their output for 2 and 3);
the default is the service's.

The CLI does the same for one file: `./main upscale in.jpg out.png`.

## In the pipeline

`consume` listens on NATS for image-sync's `image-stored` announcements and
runs each stored object through the pipeline; `backfill --prefix weeb/`
walks what is already in the bucket. Both apply the same rules:

1. Skip `<key>-orig` copies, and anything already carrying `upscaled`
   metadata (a 225px image comes back at 900px, still under the width
   threshold, so provenance is what stops a replayed event). One exception:
   an `upscaled` object that is all black is a broken run, not a result
   (the ncnn Vulkan builds of 1.5.0-1.7.0 wrote exactly that when lavapipe
   ran out of memory). When `<key>-orig` exists and is a picture, it goes
   back to the key and is upscaled again (outcome `repaired`); without a
   usable original the object is reported as `blank` and left alone. And
   a run that answers a picture with a black frame is an error: the key is
   not touched and the event goes round the retry stream.
2. Anything at least `UPSCALER_MIN_WIDTH` (1000px) wide is not upscaled,
   but is still normalised for display, once: brought down to its kind's
   display width if wider, re-encoded as JPEG at `UPSCALER_DISPLAY_QUALITY`
   if heavier than `UPSCALER_MAX_KB` (250). Nothing resizes on delivery any
   more, so a 1.3 MB poster would otherwise be 1.3 MB on every card. The
   original is kept at `<key>-orig`; a `normalized` flag stops a re-run
   touching it again.
3. Copy the object to `<key>-orig` unless that copy exists.
4. Upscale. The full result goes to `<key>-full`; the key itself gets a
   copy capped at a display width per kind (600px roots, 1000px posters
   and works, 1920px banners), because the CDN serves objects as stored.
   Metadata is carried over (image-sync's `source-length` is what keeps
   it from re-downloading the small original over the result) plus
   `upscaled`, `upscaled-from-width`, `upscaled-at` and `display-width`.
   An object upscaled before the cap existed is brought down to it on the
   next pass, with the full result kept first.
5. Purge `CDN_BASE_URL/<key>` from Cloudflare when a zone id and API token
   are set, so the resizer rebuilds its variants from the new bytes.

| Variable | Default | |
|---|---|---|
| `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY_ID`, `MINIO_SECRET_ACCESS_KEY`, `MINIO_USESSL`, `MINIO_BUCKET` | image-sync's | The bucket |
| `MINIO_PREFIX` | `weeb` | Key prefix the announced paths live under (`weeb-staging` on staging) |
| `UPSCALER_MIN_WIDTH` | `1000` | |
| `UPSCALER_KEEP_ORIGINAL` / `UPSCALER_ORIG_SUFFIX` | `true` / `-orig` | |
| `UPSCALER_BUCKET_FORMAT` | `jpg` | `webp` also works |
| `CDN_BASE_URL` | `https://cdn.weeb.vip` | |
| `CLOUDFLARE_ZONE_ID`, `CLOUDFLARE_API_TOKEN` | unset | Purge off when unset |
| `NATSURL`, `NATSCONSUMERGROUPNAME`, `NATSSUBJECT`, `NATSOFFSET` | `nats://localhost:4222`, `upscaler-service`, `image-stored`, `earliest` | |
| `UPSCALER_WORKERS` | `1` | Concurrent upscales; one per GPU |
| `UPSCALER_MAX_WIDTH_ANIME`, `_POSTER`, `_BANNER`, `_CHARACTER`, `_STAFF`, `_WORK` | 600, 1000, 1920, 600, 600, 1000 | Display width the object at the key is capped at |
| `UPSCALER_KEEP_FULL` / `UPSCALER_DISPLAY_QUALITY` | `true` / `85` | Keep the uncapped result at `<key>-full`; JPEG quality of the display copy |
| `UPSCALER_MAX_KB` | `250` | A stored object heavier than this is re-encoded at the display quality, once; 0 disables |
| `UPSCALER_SCALE_ANIME`, `_POSTER`, `_BANNER`, `_CHARACTER`, `_STAFF`, `_WORK` | unset | Scale per kind (2, 3 or 4), read off the key's folder; unset means `UPSCALER_SCALE`. A 424px MyAnimeList image at 2x lands where a 225px one did at 4x, with far less invented detail |

## The CPU runner

`runner/upscale.py` takes the ncnn flags (`-i -o -n -s -m -t -j -f`) and runs
the SRVGG graphs in `runner/models/*.onnx` with ONNX Runtime, tiled with a
10px overlap. The native factor is 4; `-s 2` or `3` scales the 4x result
down with Lanczos, as the ncnn build does. To add a checkpoint:

```sh
python -m venv .venv && .venv/bin/pip install -r runner/requirements.txt -r runner/requirements-convert.txt
.venv/bin/python runner/convert.py runner/models/realesr-general-x4v3.pth runner/models/realesr-general-x4v3.onnx
```

Only SRVGG checkpoints are convertible here (`runner/srvgg.py`); the RRDB
ones (`realesrgan-x4plus`, `-anime`) would need their architecture added
and are several times slower on a CPU.

## Models

The ncnn build ships `realesrgan-x4plus` (the default: general images,
honest on text and gradients), `realesrgan-x4plus-anime` (illustrations and
the JPEG artefacts on them; crisper line art, invents text) and `realesr-animevideov3-x2/x3/x4` (lighter
networks for anime video frames; faster, smoother, less detail). Anything
else -- waifu2x, Real-CUGAN, the community ESRGAN checkpoints -- needs its
own runner; swap the binary and model name through the variables above.

## Configuration

| Variable | Default | |
|---|---|---|
| `PORT` | `3000` | |
| `UPSCALER_BINARY` | `realesrgan-ncnn-vulkan` (image: `/app/runner/upscale.py`) | Name on PATH or a path |
| `UPSCALER_MODELS_DIR` | binary's default (image: `/app/runner/models`) | `.onnx` files for the CPU runner, `.param`/`.bin` for ncnn |
| `UPSCALER_MODEL` | `realesr-general-x4v3` | Also `realesr-general-wdn-x4v3` (denoising), `realesr-animevideov3` (lighter) |
| `UPSCALER_SCALE` | `2` | 2, 3 or 4. The key is capped at a display width anyway, and 4x of a poster on lavapipe needs more than 3 GB |
| `UPSCALER_GPU` | `auto` | Vulkan device index. There is no "CPU" value: a software Vulkan driver (Mesa's lavapipe) is device 0, which `auto` picks; `-1` makes the binary answer "invalid gpu device" |
| `UPSCALER_TILE` | `0` | Smaller tiles use less memory |
| `UPSCALER_THREADS` | unset | The binary's `-j load:proc:save`, e.g. `1:2:1`. Processing threads are what lavapipe's memory scales with |
| `UPSCALER_FORMAT` | `png` | Default output when the request names none |
| `UPSCALER_TIMEOUT` | `10m` | Per image |
| `UPSCALER_MAX_UPLOAD_MB` | `20` | |

## Running locally

Download the macOS or Linux build from the Real-ESRGAN releases page
(`realesrgan-ncnn-vulkan-20220424-<os>.zip`), unzip it, then:

```sh
make run ESRGAN=/path/to/unzipped
```

On an Apple Silicon Mac the build runs on the GPU through MoltenVK. A 225x318
poster upscales to 900x1272 in about a second.

## In the cluster

The image runs the ONNX model on the CPU (see "The CPU runner"): a 424px
MyAnimeList image at 2x takes about 35 seconds on one 4-thread worker of a
2013 Xeon, within 2 GB. There is no Vulkan in the image any more; the
lavapipe builds needed more than 3 GB per poster and wrote black frames
when they could not get it.
