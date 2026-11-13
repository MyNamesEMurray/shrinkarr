FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /tinydarr .

# linuxserver/ffmpeg: ffmpeg 8.x with NVENC/NVDEC, libvmaf and the dovi_rpu bsf.
FROM lscr.io/linuxserver/ffmpeg:latest
COPY --from=build /tinydarr /usr/local/bin/tinydarr
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
EXPOSE 8265
ENTRYPOINT ["/usr/local/bin/tinydarr"]
