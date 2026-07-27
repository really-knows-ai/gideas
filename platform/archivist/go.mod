module github.com/foundry/flow/archivist

go 1.26

require (
	github.com/foundry/flow/gen v0.0.0
	github.com/foundry/flow/pkg/eventbus v0.0.0-00010101000000-000000000000
	github.com/foundry/flow/pkg/randid v0.0.0
	github.com/foundry/flow/pkg/sqldbutil v0.0.0-00010101000000-000000000000
	github.com/foundry/flow/sdk/go v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/mattn/go-sqlite3 v1.14.34
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	github.com/LadybugDB/go-ladybug v0.17.0 // indirect
	github.com/apache/arrow-go/v18 v18.6.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
)

replace github.com/foundry/flow/gen => ../../gen

replace github.com/foundry/flow/pkg/eventbus => ../pkg/eventbus

replace github.com/foundry/flow/pkg/randid => ../pkg/randid

replace github.com/foundry/flow/pkg/sqldbutil => ../pkg/sqldbutil

replace github.com/foundry/flow/sdk/go => ../../sdk/go
