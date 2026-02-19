module github.com/gideas/flow/nodes

go 1.25.0

require (
	github.com/gideas/flow/gen v0.0.0
	github.com/gideas/flow/sdk/go v0.0.0
	google.golang.org/grpc v1.79.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	github.com/gideas/flow/gen => ../gen
	github.com/gideas/flow/sdk/go => ../sdk/go
)
