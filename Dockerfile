FROM umputun/baseimage:buildgo-latest AS build

ARG GIT_BRANCH
ARG GITHUB_SHA
ARG CI

ENV CGO_ENABLED=0


ADD . /build
WORKDIR /build

RUN \
    if [ -z "$CI" ] ; then \
    echo "runs outside of CI" && version=$(git rev-parse --abbrev-ref HEAD)-$(git log -1 --format=%h)-$(date +%Y%m%dT%H:%M:%S); \
    else version=${GIT_BRANCH}-${GITHUB_SHA:0:7}-$(date +%Y%m%dT%H:%M:%S); fi && \
    echo "version=$version" && \
    cd app && go build -o /build/docker-logger -ldflags "-X main.revision=${version} -s -w"


FROM umputun/baseimage:app-latest
LABEL org.opencontainers.image.source="https://github.com/umputun/docker-logger"
ENV APP_UID=995
COPY --from=build /build/docker-logger /srv/docker-logger
RUN \
    chown -R app:app /srv && \
    chmod +x /srv/docker-logger
WORKDIR /srv

VOLUME ["/srv/logs"]
CMD ["/srv/docker-logger"]
ENTRYPOINT ["/init.sh"]
