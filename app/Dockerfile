# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off + static link so the binary runs on a distroless/scratch base.
# -trimpath and -s -w keep the binary small and reproducible.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api .

# --- runtime stage ---------------------------------------------------------
# distroless/static:nonroot has no shell or package manager (small attack
# surface) and runs as an unprivileged user (uid 65532) by default, which
# satisfies runAsNonRoot and readOnlyRootFilesystem in the Deployment.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/api /api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
