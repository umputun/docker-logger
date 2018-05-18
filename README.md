# docker-logger [![Docker Automated build](https://img.shields.io/docker/automated/jrottenberg/ffmpeg.svg)](https://hub.docker.com/r/umputun/docker-logger/)

**docker-logger** is a small application collecting logs from other containers on the host that are started without
the `-t` option and are configured with a logging driver that works with docker logs (journald and json-file).
It can forward both stdout and stderr of containers to local, rotated files and/or to remote syslog.

## Install

Copy provided [docker-compose.yml](https://github.com/umputun/docker-logger/blob/master/docker-compose.yml), customize if needed and run it with `docker-compose up -d`. By default `docker-logger` will collect all logs from containers and put it to `./logs` directory.

## Customization

All changes can be done via container's environment in `docker-compose.yml` or with command line

| Command line              | Environment            | Default                     | Description                               |
| ---------------           | -------------          | --------------------------- | ----------------------------------------- |
| `--docker`                | `DOCKER_HOST`          | unix:///var/run/docker.sock | docker host                               |
| `--syslog-host`           | `SYSLOG_HOST`          | 127.0.0.1:514               | syslog remote host (udp4)                 |
| `--files`                 | `LOG_FILES`            | No                          | enable logging to files                   |
| `--syslog`                | `LOG_SYSLOG`           | No                          | enable logging to syslog                  |
| `--max-size`              | `MAX_SIZE`             | 10                          | size of log triggering rotation (MB)      |
| `--max-files`             | `MAX_FILES`            | 5                           | number of rotated files to retain         |
| `--max-age`               | `MAX_AGE`              | 30                          | maximum number of days to retain          |
| `--exclude`               | `EXCLUDE`              |                             | excluded container names, comma separated |
| `--time-rotate `          | `TimeRotate`           | No                          | enable time roate mode                    |
| `--time-rotate-duration`  | `TIME_ROTATE_DURATION]`| 300                         | time roate duration second                |
| `--time-rotate-format`    | `TIME_ROTATE_FORMAT`   | %Y-%m-%d_%H-%M              | time roate format                         |
|                           | `TIME_ZONE`            | UTC                         | Time zone for container                   |


- at least one of destinations (`files` or `syslog`) should be allowed
- location of log files can be mapped to host via `volume`, ex: `- ./logs:/srv/logs` (see `docker-compose.yml`)

## Build from the source

- clone this repo - `git clone https://github.com/umputun/docker-logger.git`
- build the logger - `cd docker-logger && docker build -t umputun/docker-logger .`
- try it - `docker run -it --rm -v $(pwd)/logs:/srv/logs -v /var/run/docker.sock:/var/run/docker.sock umputun/docker-logger /srv/docker-logger --files`
