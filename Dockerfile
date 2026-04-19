FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /swarmex-deployer ./cmd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
COPY --from=build /swarmex-deployer /usr/local/bin/swarmex-deployer
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://localhost:8080/health || exit 1
ENTRYPOINT ["swarmex-deployer"]
