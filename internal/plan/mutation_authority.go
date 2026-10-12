package plan

import "context"

type beforeOperationKey struct{}

// WithBeforeOperation carries an optional authority check to every provider
// operation in an Apply batch. Callers retain ownership of the check and its
// external state; providers invoke it before each mutation.
func WithBeforeOperation(ctx context.Context, check func(context.Context) error) context.Context {
	if check == nil {
		return ctx
	}
	return context.WithValue(ctx, beforeOperationKey{}, check)
}

func CheckBeforeOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(beforeOperationKey{}).(func(context.Context) error); ok {
		return check(ctx)
	}
	return nil
}
