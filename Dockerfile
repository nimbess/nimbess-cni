FROM golang:1.11
WORKDIR $GOPATH/src/github.com/nimbess/nimbess-cni
COPY . .
RUN ./build/build-go.sh

FROM centos:latest
COPY --from=0 /go/src/github.com/nimbess/nimbess-cni/k8s/install-cni.sh .
WORKDIR /opt/cni/bin
COPY --from=0 /go/src/github.com/nimbess/nimbess-cni/bin/nimbess .
WORKDIR /
