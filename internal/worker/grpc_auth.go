package worker

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ctxKey string

const tokenCtxKey ctxKey = "worker-token"

// WorkerAuthInterceptor returns a gRPC unary interceptor that extracts
// a Bearer token from the "authorization" metadata and stores it in the context.
// Rejects with Unauthenticated if no token is provided.
func WorkerAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		values := md.Get("authorization")
		if len(values) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization token")
		}

		token := values[0]
		// Strip "Bearer " prefix if present
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}

		ctx = context.WithValue(ctx, tokenCtxKey, token)
		return handler(ctx, req)
	}
}

// TokenFromContext extracts the raw token string set by WorkerAuthInterceptor.
func TokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tokenCtxKey).(string)
	return v
}

// workerCredentials implements grpc.PerRPCCredentials to attach a Bearer token
// to every outgoing gRPC call from a worker.
type workerCredentials struct {
	token string
}

func (c workerCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (c workerCredentials) RequireTransportSecurity() bool { return false }
