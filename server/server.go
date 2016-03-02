package server

import (
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"src.sourcegraph.com/sourcegraph/go-sourcegraph/sourcegraph"
	"src.sourcegraph.com/sourcegraph/server/internal/middleware/inner"
	"src.sourcegraph.com/sourcegraph/server/internal/middleware/outer"
	"src.sourcegraph.com/sourcegraph/svc"
)

// NewServer creates a new gRPC server with all RPC services
// registered. Callers are responsible for setting up the listener and
// any other server options.
func NewServer(svcs svc.Services, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(opts, grpc.CustomCodec(sourcegraph.GRPCCodec))
	s := grpc.NewServer(opts...)
	svc.RegisterAll(s, svcs)
	return s
}

func Config(ctxFunc func(context.Context) context.Context) svc.Services {
	// Construct the inner services. The inner services are the
	// services as they appear in the context of service method
	// implementations. Below we wrap them with metadata, config,
	// etc., handlers that only need to be run once per external
	// request.
	services := inner.Services()

	// Wrap in middleware for context initialization. This is the
	// outermost wrapper because it performs the most
	// expensive work, and we only want it to be run once per external
	// request (it does not need to be re-run when services make
	// internal requests to their own methods or other services'
	// methods).
	return outer.Services(ctxFunc, services)
}
