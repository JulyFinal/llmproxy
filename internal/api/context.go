package api

import (
	"context"

	"proxyllm/internal/domain"
)

// Private context key types — prevents collision with any third-party middleware.
type (
	ctxKeyProxy     struct{}
	ctxKeyRequestID struct{}
	ctxKeySessionID struct{}
)

func withProxyCtx(ctx context.Context, pc *domain.ProxyContext) context.Context {
	return context.WithValue(ctx, ctxKeyProxy{}, pc)
}

func proxyCtxFrom(ctx context.Context) *domain.ProxyContext {
	v, _ := ctx.Value(ctxKeyProxy{}).(*domain.ProxyContext)
	return v
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, id)
}

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID{}).(string)
	return v
}

func withSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID{}, id)
}

func sessionIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeySessionID{}).(string)
	return v
}
