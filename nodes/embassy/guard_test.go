package main

import (
	flow "github.com/gideas/flow/sdk/go"
)

// Compile-time guard: embassyHandler must implement the SDK's
// EmbassyServiceHandler interface. If any method is removed or its
// signature changes, this line fails to compile.
var _ flow.EmbassyServiceHandler = (*embassyHandler)(nil)
