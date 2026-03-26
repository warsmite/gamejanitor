package agent

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type ctxKey string

const tokenCtxKey ctxKey = "worker-token"

// WorkerAuthInterceptor returns a gRPC unary interceptor that extracts
// a Bearer token from the "authorization" metadata and stores it in the context.
// Rejects with Unauthenticated if no token is provided.
// All RPCs except Register require a verified client certificate (mTLS).
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

		// Register is the enrollment RPC — token auth only, no client cert required.
		// All other RPCs require a verified client certificate.
		if info.FullMethod != "/worker.ControllerService/Register" {
			p, ok := peer.FromContext(ctx)
			if !ok || p.AuthInfo == nil {
				return nil, status.Error(codes.Unauthenticated, "client certificate required")
			}
			tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
			if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
				return nil, status.Error(codes.Unauthenticated, "client certificate required")
			}
		}

		return handler(ctx, req)
	}
}

// TokenFromContext returns the raw token string set by WorkerAuthInterceptor.
func TokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tokenCtxKey).(string)
	return v
}

// WorkerCredentials implements grpc.PerRPCCredentials to attach a Bearer token
// to every outgoing gRPC call from a worker.
type WorkerCredentials struct {
	Token      string
	RequireTLS bool
}

func (c WorkerCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.Token}, nil
}

func (c WorkerCredentials) RequireTransportSecurity() bool { return c.RequireTLS }
