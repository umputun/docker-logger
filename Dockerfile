FROM umputun/baseimage:buildgo-latest as build

ARG CI
ARG COVERALLS_TOKEN
ARG TRAVIS
ARG TRAVIS_BRANCH
ARG TRAVIS_COMMIT
ARG TRAVIS_JOB_ID
ARG TRAVIS_JOB_NUMBER
ARG TRAVIS_OS_NAME
ARG TRAVIS_PULL_REQUEST
ARG TRAVIS_PULL_REQUEST_SHA
ARG TRAVIS_REPO_SLUG
ARG TRAVIS_TAG

ADD . /build/docker-logger
WORKDIR /build/docker-logger

RUN cd app && go test -v -mod=vendor -covermode=count -coverprofile=/profile.cov ./...

RUN golangci-lint run --out-format=tab --tests=false ./...

RUN \
    revison=$(/script/git-rev.sh) && \
    echo "revision=${revison}" && \
    go build -mod=vendor -o docker-logger -ldflags "-X main.revision=$revison -s -w" ./app

# submit coverage to coverals if COVERALLS_TOKEN in env
RUN if [ -z "$COVERALLS_TOKEN" ] ; then echo "coverall not enabled" ; \
    else goveralls -coverprofile=/profile.cov -service=travis-ci -repotoken $COVERALLS_TOKEN || echo "coverall failed!"; fi


FROM alpine:3.9

RUN apk add --update --no-cache tzdata

COPY --from=build /build/docker-logger /srv/
COPY init.sh /init.sh
RUN chmod +x /init.sh

WORKDIR /srv

VOLUME ["/srv/logs"]
CMD ["/srv/docker-logger"]
ENTRYPOINT ["/init.sh"]
