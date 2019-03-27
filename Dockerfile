FROM umputun/baseimage:buildgo-latest as build

ADD . /build/docker-logger
WORKDIR /build/docker-logger

RUN cd app && go test -v -mod=vendor ./...

RUN golangci-lint run --out-format=tab --disable-all --tests=false --enable=unconvert \
    --enable=megacheck --enable=structcheck --enable=gas --enable=gocyclo --enable=dupl --enable=misspell \
    --enable=unparam --enable=varcheck --enable=deadcode --enable=typecheck \
    --enable=ineffassign --enable=varcheck ./...

RUN \
    revison=$(/script/git-rev.sh) && \
    echo "revision=${revison}" && \
    go build -mod=vendor -o docker-logger -ldflags "-X main.revision=$revison -s -w" ./app


FROM umputun/baseimage:app-latest

COPY --from=build /build/docker-logger /srv/
COPY init.sh /init.sh
RUN chmod +x /init.sh

WORKDIR /srv

VOLUME ["/srv/logs"]
CMD ["/srv/docker-logger"]
ENTRYPOINT ["/init.sh"]