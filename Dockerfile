# --- Build frontend ---
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Build backend ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/showdown .

# --- Runtime ---
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/showdown /showdown
EXPOSE 8080
ENTRYPOINT ["/showdown"]
