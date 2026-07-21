module github.com/foundry/flow/archivist

go 1.25.3

require (
	github.com/foundry/flow/gen v0.0.0
	github.com/foundry/flow/pkg/eventbus v0.0.0-00010101000000-000000000000
	github.com/foundry/flow/pkg/randid v0.0.0-00010101000000-000000000000
	github.com/foundry/flow/pkg/sqldbutil v0.0.0-00010101000000-000000000000
	github.com/foundry/flow/sdk/go v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/mattn/go-sqlite3 v1.14.34
	google.golang.org/grpc v1.79.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
)

replace github.com/foundry/flow/gen => ../../gen

replace github.com/foundry/flow/pkg/eventbus => ../pkg/eventbus

replace github.com/foundry/flow/pkg/randid => ../pkg/randid

replace github.com/foundry/flow/pkg/sqldbutil => ../pkg/sqldbutil

replace github.com/foundry/flow/sdk/go => ../../sdk/go
