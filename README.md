# upscaler-service

Upscales artwork with [Real-ESRGAN](https://github.com/xinntao/Real-ESRGAN).
The default model is the general `realesrgan-x4plus`: the catalogue is
promotional posters that mix illustration, painted backgrounds, logos and
typography, and the anime-trained model redraws all of it as flat colour and
clean strokes, turning small text into invented letter shapes. The general
model keeps text and gradients honest at the cost of slightly softer line
art; `UPSCALER_MODEL=realesrgan-x4plus-anime` switches back.

The service is a thin HTTP wrapper around the authors' `realesrgan-ncnn-vulkan`
build. That build is one static executable that runs on a GPU through Vulkan
or on a CPU through a software Vulkan driver; no Python, no CUDA.

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
   threshold, so provenance is what stops a replayed event).
2. Skip anything at least `UPSCALER_MIN_WIDTH` (1000px) wide. Every 225px
   MyAnimeList image and every 680px TheTVDB poster is below it.
3. Copy the object to `<key>-orig` unless that copy exists.
4. Upscale, write back over the same key as JPEG with the original's
   metadata carried over (image-sync's `source-length` is what keeps it
   from re-downloading the small original over the result) plus
   `upscaled`, `upscaled-from-width` and `upscaled-at`.
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
| `UPSCALER_SCALE_ANIME`, `_POSTER`, `_BANNER`, `_CHARACTER`, `_STAFF`, `_WORK` | unset | Scale per kind (2, 3 or 4), read off the key's folder; unset means `UPSCALER_SCALE`. A 424px MyAnimeList image at 2x lands where a 225px one did at 4x, with far less invented detail |

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
| `UPSCALER_BINARY` | `realesrgan-ncnn-vulkan` | Name on PATH or a path |
| `UPSCALER_MODELS_DIR` | binary's default | The `.param`/`.bin` directory |
| `UPSCALER_MODEL` | `realesrgan-x4plus` | Also `realesrgan-x4plus-anime`, `realesr-animevideov3-x{2,3,4}` |
| `UPSCALER_SCALE` | `4` | What the model was trained for |
| `UPSCALER_GPU` | `auto` | Vulkan device index. There is no "CPU" value: a software Vulkan driver (Mesa's lavapipe) is device 0, which `auto` picks; `-1` makes the binary answer "invalid gpu device" |
| `UPSCALER_TILE` | `0` | Smaller tiles use less memory |
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

The image installs Mesa's lavapipe, so a pod with no GPU still works, only
slowly (expect tens of seconds per poster). A node with a GPU and its Vulkan
ICD mounted makes the same image fast; nothing in the service changes.
