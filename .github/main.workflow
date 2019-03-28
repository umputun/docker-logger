workflow "Build" {
  on = "push"
  resolves = ["GitHub Action for Docker"]
}

action "GitHub Action for Docker" {
  uses = "actions/docker/cli@8cdf801b322af5f369e00d85e9cf3a7122f49108"
  secrets = ["COVERALLS_TOKEN"]
  args = "build --build-arg COVERALLS_TOKEN=$COVERALLS_TOKEN --build-arg CI=$CI -t umputun/docker-logger ."
}
