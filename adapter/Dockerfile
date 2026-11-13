# Adapter image. Build from the repository root so the Connect chart can be
# packaged into the binary before go build:
#   docker build -f adapter/Dockerfile -t ghcr.io/percona/percona-services-adapter:dev .
FROM golang:1.26.10-alpine AS build

WORKDIR /src

COPY adapter/go.mod adapter/go.sum ./
RUN go mod download

COPY adapter/cmd ./cmd
COPY adapter/internal ./internal
COPY charts/pds /tmp/pds
# helm package refuses a Chart.yaml whose charts/ directory is empty. The
# tarball is the chart tree; bootstrap runs helm dependency update after it
# extracts, so OpenEverest and PMM are not baked into this image.
RUN tar -C /tmp -czf ./internal/api/connect_chart.tgz pds

# Static binary so the runtime image can stay distroless.
ENV CGO_ENABLED=0
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/adapter ./cmd/adapter

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/adapter /usr/local/bin/adapter

# 65532 is the distroless nonroot user, matched by the chart security context.
USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/adapter"]
