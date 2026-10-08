# syntax=docker/dockerfile:1
#
# The docuconf CLI as a static binary on distroless, published as
# ghcr.io/docuconf/docuconf by .github/workflows/release.yml (linux/amd64 and
# linux/arm64). Build it locally from this directory:
#
#   docker build -t docuconf cmd/docuconf
#
# To put the CLI into another image:
#
#   COPY --from=ghcr.io/docuconf/docuconf:<version> /docuconf /usr/local/bin/docuconf

# Cross-compile on the build machine's own architecture; no emulation needed.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /docuconf .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /docuconf /docuconf
ENTRYPOINT ["/docuconf"]
