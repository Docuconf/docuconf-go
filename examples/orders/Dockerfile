# The orders service as a container image, for the end-to-end test in
# deploy/terraform/e2e. Build it from the repository root, because go.mod
# builds against the SDK in this repository:
#
#   docker build -f examples/orders/Dockerfile -t orders-api:e2e .
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY examples/orders/go.mod examples/orders/go.sum examples/orders/
RUN cd examples/orders && go mod download
COPY . .
RUN cd examples/orders && CGO_ENABLED=0 go build -trimpath -o /orders .

# Runs as uid 65532. The pod's fsGroup makes the mounted TLS Secret, which
# the module mounts with mode 0400, readable to it.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /orders /orders
ENTRYPOINT ["/orders"]
