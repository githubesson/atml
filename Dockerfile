FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/atml ./cmd/atml && mkdir /out/data

FROM scratch
COPY --from=build --chown=65532:65532 /out/atml /atml
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
EXPOSE 8080
ENV ATML_ADDR=:8080 ATML_DATA_DIR=/data
USER 65532:65532
ENTRYPOINT ["/atml", "serve"]
