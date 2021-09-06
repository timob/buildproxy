Build Proxy
===
This cli command is used to rebuild source code while developing a local HTTP server.
The configuration file path defaults to `~/.buildproxy.yaml`. It consists of an array of configuraion stanzas like:
``` yaml
- name: "hello"
  start_command: "./hello --listen=:6000"
  build_command: "go build"
  destination_url: "http://localhost:6000"
  listen_address: ":6001"
  watch_path: "/User/user/repos/hello"
  file_extensions:
  - ".go"
  exclude_paths:
  - "test"

```
The command to use this configuration is `buildproxy hello`
. It will build a list of sub directories under `/User/user/repos/hello` excluding `test`. It will then start a server on port 6001 proxying all requests to `http://localhost:6000`. When a request comes in if there are any changes to `.go` files in the subdir list since the last time the `go build` was run (or if it has not been run), it will run this build command and then run `./hello --listen=:6000` (stopping the old process if needed). Any build errors will be output to standard error. 

You can add more stanzas for different configurations. 

Install
---
``` bash
go get github.com/timob/buildproxy
go install
```
