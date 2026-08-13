module github.com/foundry/flow/queue

go 1.26.0

require (
	github.com/foundry/flow/gen v0.0.0
	github.com/foundry/flow/sdk/go v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.80.0
)

require (
	github.com/foundry/flow/pkg/metadata v0.0.0 // indirect
	github.com/mattn/go-sqlite3 v1.14.34 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/foundry/flow/gen => ../../gen

replace github.com/foundry/flow/pkg/metadata => ../pkg/metadata

replace github.com/foundry/flow/sdk/go => ../../sdk/go
