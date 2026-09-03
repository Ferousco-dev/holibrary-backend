# Multi-stage build. The toolchain is ~800 MB; none of it ships.
# The final image is the static binary plus CA certificates (NFR-011).

# ---- build stage ------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded before the source, so an edit to a .go
# file reuses the cached module layer instead of re-downloading everything.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary with no libc dependency, which is what
# lets the final stage be scratch rather than a full distribution.
# -trimpath keeps local filesystem paths out of the binary.
# -s -w drop the symbol table and DWARF data, roughly halving the size.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /holibrary-api ./cmd/api

# ---- runtime stage ----------------------------------------------------------
FROM scratch

# Outbound TLS to Neon, Upstash, Resend and FCM needs a trust store; scratch has
# none, so the roots are copied from the build image.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /holibrary-api /holibrary-api

# There is no shell, no package manager and no coreutils in this image. An
# attacker who achieves code execution finds nothing to execute.
EXPOSE 8080

ENTRYPOINT ["/holibrary-api"]
