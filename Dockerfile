FROM golang:1.9-alpine as build

RUN go version

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

RUN \
    apk add --no-cache --update git &&\
    go get -u gopkg.in/alecthomas/gometalinter.v1 && \
    ln -s /go/bin/gometalinter.v1 /go/bin/gometalinter && \
    gometalinter --install --force

ADD . /go/src/github.com/umputun/docker-logger
WORKDIR /go/src/github.com/umputun/docker-logger

RUN cd app && go test -v $(go list -e ./... | grep -v vendor)

RUN gometalinter --disable-all --deadline=300s --vendor --enable=vet --enable=vetshadow --enable=golint \
    --enable=staticcheck --enable=ineffassign --enable=goconst --enable=errcheck --enable=unconvert \
    --enable=deadcode  --enable=gosimple --exclude=test --exclude=mock --exclude=vendor ./...

RUN go build -o docker-logger -ldflags "-X main.revision=$(git rev-parse --abbrev-ref HEAD)-$(git describe --abbrev=7 --always --tags)-$(date +%Y%m%d-%H:%M:%S) -s -w" ./app


FROM alpine:3.7

RUN apk add --update --no-cache tzdata

COPY --from=build /go/src/github.com/umputun/docker-logger/docker-logger /srv/
COPY init.sh /srv/init.sh
RUN chmod +x /srv/init.sh

USER root
WORKDIR /srv

VOLUME ["/srv/logs"]
CMD ["/srv/docker-logger"]
ENTRYPOINT ["/srv/init.sh"]