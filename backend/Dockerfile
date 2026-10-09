# Build a static Go binary, then ship it on a minimal base. Build context is `backend/`.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/api /usr/local/bin/api
# Run as a non-root user.
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["api"]
