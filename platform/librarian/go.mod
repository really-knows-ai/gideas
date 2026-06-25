module github.com/gideas/flow/librarian

go 1.25.0

require (
	github.com/asg017/sqlite-vec-go-bindings v0.1.6
	github.com/gideas/flow/gen v0.0.0
	github.com/gideas/flow/pkg/eventbus v0.0.0-00010101000000-000000000000
	github.com/gideas/flow/pkg/randid v0.0.0-00010101000000-000000000000
	github.com/mattn/go-sqlite3 v1.14.34
	google.golang.org/grpc v1.79.1
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
)

replace github.com/gideas/flow/gen => ../../gen

replace github.com/gideas/flow/pkg/eventbus => ../pkg/eventbus

replace github.com/gideas/flow/pkg/randid => ../pkg/randid
