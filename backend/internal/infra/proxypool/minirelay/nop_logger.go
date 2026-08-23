package minirelay

import (
	"context"

	"github.com/sagernet/sing/common/logger"
)

// nopLogger 是 sing logger 接口的 no-op 实现,同时实现 ContextLogger。
// sing-box v1.12 的 tls.NewClient 需要 ContextLogger 参数。
type nopLogger struct{}

// Logger 接口
func (nopLogger) Trace(args ...any)                 {}
func (nopLogger) Debug(args ...any)                 {}
func (nopLogger) Info(args ...any)                  {}
func (nopLogger) Warn(args ...any)                  {}
func (nopLogger) Error(args ...any)                 {}
func (nopLogger) Fatal(args ...any)                 {}
func (nopLogger) Panic(args ...any)                 {}

// Logger 接口的 Printf 变体(部分调用方用)
func (nopLogger) Tracef(format string, args ...any) {}
func (nopLogger) Debugf(format string, args ...any) {}
func (nopLogger) Infof(format string, args ...any)  {}
func (nopLogger) Warnf(format string, args ...any)  {}
func (nopLogger) Errorf(format string, args ...any) {}
func (nopLogger) Fatalf(format string, args ...any) {}
func (nopLogger) Panicf(format string, args ...any) {}

// ContextLogger 接口
func (nopLogger) TraceContext(_ context.Context, args ...any) {}
func (nopLogger) DebugContext(_ context.Context, args ...any)  {}
func (nopLogger) InfoContext(_ context.Context, args ...any)   {}
func (nopLogger) WarnContext(_ context.Context, args ...any)   {}
func (nopLogger) ErrorContext(_ context.Context, args ...any)  {}
func (nopLogger) FatalContext(_ context.Context, args ...any) {}
func (nopLogger) PanicContext(_ context.Context, args ...any) {}

var (
	_ logger.Logger        = nopLogger{}
	_ logger.ContextLogger = nopLogger{}
)
