FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /shrinkarr .

# linuxserver/ffmpeg: ffmpeg 8.x with NVENC/NVDEC, QuickSync, VAAPI, x265, SVT-AV1, libvmaf and dovi_rpu.
FROM lscr.io/linuxserver/ffmpeg:latest
COPY --from=build /shrinkarr /usr/local/bin/shrinkarr
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility \
    CONFIG_DIR=/config
VOLUME /config
EXPOSE 8265
ENTRYPOINT ["/usr/local/bin/shrinkarr"]
