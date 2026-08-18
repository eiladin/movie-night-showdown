# --- Build frontend ---
# Pinned to BUILDPLATFORM: the bundle is static JS and CSS, identical for every
# target architecture. Without the pin, buildx runs this stage once per platform
# and the non-native pass runs npm under QEMU emulation to produce byte-identical
# output.
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Build backend ---
# Also pinned to BUILDPLATFORM, and cross-compiled via GOARCH instead. Go needs
# no cross-toolchain here because CGO is disabled, so a native compiler produces
# the target binary in seconds where emulation took minutes.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
# Supplied by buildx for each platform in the bake target.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/showdown .

# --- Runtime ---
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/showdown /showdown
EXPOSE 8080
ENTRYPOINT ["/showdown"]
