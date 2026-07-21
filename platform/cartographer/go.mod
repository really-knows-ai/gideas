module github.com/foundry/flow/cartographer

go 1.25.3

replace github.com/foundry/flow/gen => ../../gen

require (
	github.com/foundry/flow/gen v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
)

require (
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/grpc v1.79.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
