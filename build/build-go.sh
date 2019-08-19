#!/usr/bin/env bash
set -eu
eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")
GOOS=${GOOS:-${GOHOSTOS}}
GOARCH=${GOACH:-${GOHOSTARCH}}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}
CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} GO111MODULE=on go build ${GOFLAGS} -ldflags "${GLDFLAGS}" -o bin/nimbess cmd/nimbess-cni.go
