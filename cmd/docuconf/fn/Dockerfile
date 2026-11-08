# syntax=docker/dockerfile:1
#
# function-docuconf's runtime image. Build it, and the Crossplane package,
# from the repository root, with cmd/docuconf, the module it shares code
# with, as the context:
#
#   docker build -f cmd/docuconf/fn/Dockerfile -t function-docuconf cmd/docuconf
#   crossplane xpkg build -f cmd/docuconf/fn/package --embed-runtime-image=function-docuconf
#
# Cross-compile on the build machine's own architecture; no emulation needed.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
WORKDIR /src/fn
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags=-s -o /function .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /function /function
EXPOSE 9443
ENTRYPOINT ["/function"]
