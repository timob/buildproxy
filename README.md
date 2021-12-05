Build Proxy
===
This cli command is used to rebuild source code while developing a local HTTP server.
The configuration file path defaults to `~/.buildproxy.yaml`. It consists of an array of configuration stanzas like:
``` yaml
- name: "grafanatut"
  watch_path: "/Users/user/repos/grafana-tutorial-environment/app"
  file_extensions:
  - ".go"
  build_command:
    argv:
    - "go"
    - "build"
    env:
    - "GOARCH=amd64"
    - "GOOS=linux"
  start_command:
    work_dir: ".."
    argv:
    - "docker-compose"
    - "up"
    - "-d"
  destination_url: "http://localhost:8081"
  listen_address: ":8083"
```
The command to use this configuration is `buildproxy grafanatut`
. It will build a list of sub directories under `/Users/user/repos/grafana-tutorial-environment/app`. It will then start a server on port 8083 proxying all requests to `http://localhost:8081`. When a request comes in if there are any changes to `.go` files in the subdir list since the last time the `go build` was run (or if it has not been run), it will run this build command and then run `docker-compose up -d` (stopping the old process if needed). Any build errors will be output to standard error.

You can add more stanzas for different configurations.

Install
---
``` bash
go get github.com/timob/buildproxy
go install
```
