package main

import "context"

func withUser(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, userCtxKey{}, username)
}

func userFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userCtxKey{}).(string)
	return v, ok
}
