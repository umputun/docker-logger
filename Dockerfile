FROM umputun/baseimage:buildgo-latest as build

ADD . /go/src/github.com/umputun/docker-logger
WORKDIR /go/src/github.com/umputun/docker-logger
RUN go build -o docker-logger -ldflags "-X main.revision=$(git rev-parse --abbrev-ref HEAD)-$(git describe --abbrev=7 --always --tags)-$(date +%Y%m%d-%H:%M:%S) -s -w" ./app


FROM umputun/baseimage:micro-latest

COPY --from=build /go/src/github.com/umputun/docker-logger/docker-logger /srv/
COPY init.sh /srv/init.sh
RUN chmod +x /srv/init.sh

USER root
WORKDIR /srv

VOLUME ["/srv/logs"]
CMD ["/srv/docker-logger"]
