module github.com/foundry/flow/eventbus

go 1.25.3

require (
	github.com/foundry/flow/gen v0.0.0
	github.com/foundry/flow/pkg/randid v0.0.0
	github.com/foundry/flow/pkg/sqldbutil v0.0.0-00010101000000-000000000000
	github.com/mattn/go-sqlite3 v1.14.34
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	go.opentelemetry.io/otel/metric v1.41.0 // indirect
	go.opentelemetry.io/otel/sdk v1.40.0 // indirect
	go.opentelemetry.io/otel/trace v1.41.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
)

replace github.com/foundry/flow/gen => ../../gen

replace github.com/foundry/flow/pkg/randid => ../pkg/randid

replace github.com/foundry/flow/pkg/sqldbutil => ../pkg/sqldbutil
