# upscaler-service

Upscales anime artwork with [Real-ESRGAN](https://github.com/xinntao/Real-ESRGAN),
using the `realesrgan-x4plus-anime` model: trained on anime illustrations and
on removing JPEG artefacts from them, which is what the 225px MyAnimeList
images the scraper stores need.

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
     'http://localhost:3000/upscale?format=webp' -o poster-4x.webp
```

The CLI does the same for one file: `./main upscale in.jpg out.png`.

## Configuration

| Variable | Default | |
|---|---|---|
| `PORT` | `3000` | |
| `UPSCALER_BINARY` | `realesrgan-ncnn-vulkan` | Name on PATH or a path |
| `UPSCALER_MODELS_DIR` | binary's default | The `.param`/`.bin` directory |
| `UPSCALER_MODEL` | `realesrgan-x4plus-anime` | Also `realesrgan-x4plus`, `realesr-animevideov3-x{2,3,4}` |
| `UPSCALER_SCALE` | `4` | What the model was trained for |
| `UPSCALER_GPU` | `auto` | Device index; `-1` forces CPU |
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
