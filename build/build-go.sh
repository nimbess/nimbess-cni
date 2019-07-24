#!/usr/bin/env bash
set -eu
eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")
echo $GOPATH
GOOS=${GOOS:-${GOHOSTOS}}
GOARCH=${GOACH:-${GOHOSTARCH}}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}
GO111MODULE=on CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} go build ${GOFLAGS} -ldflags "${GLDFLAGS}" -o bin/nimbess cmd/nimbess-cni.go
