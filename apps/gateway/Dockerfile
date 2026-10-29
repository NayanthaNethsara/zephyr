FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/zephyr ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zephyr /zephyr
EXPOSE 8080
ENTRYPOINT ["/zephyr"]
