FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /shrinkarr .

# linuxserver/ffmpeg: ffmpeg 8.x with NVENC/NVDEC, libvmaf and the dovi_rpu bsf.
FROM lscr.io/linuxserver/ffmpeg:latest
COPY --from=build /shrinkarr /usr/local/bin/shrinkarr
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
EXPOSE 8265
ENTRYPOINT ["/usr/local/bin/shrinkarr"]
