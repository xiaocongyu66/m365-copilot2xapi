package log

import (
	"fmt"
	"log/slog"
	"os"
)

// proxypool 的 log 包适配层:把 logrus 调用桥接到 slog,
// 避免引入 sirupsen/logrus 依赖。保留 proxypool 原有的函数签名。

const (
	ERROR = iota
	WARN
	INFO
	DEBUG
	TRACE
)

var level = INFO

func SetLevel(l int) { level = l }

// Infoln 模拟 logrus.Infoln
func Infoln(args ...any) {
	if level >= INFO {
		slog.Info("", "msg", fmt.Sprint(args...))
	}
}

// Warnln 模拟 logrus.Warnln
func Warnln(args ...any) {
	if level >= WARN {
		slog.Warn("", "msg", fmt.Sprint(args...))
	}
}

// Errorln 模拟 logrus.Errorln
func Errorln(args ...any) {
	if level >= ERROR {
		slog.Error("", "msg", fmt.Sprint(args...))
	}
}

// Debugln 模拟 logrus.Debugln
func Debugln(args ...any) {
	if level >= DEBUG {
		slog.Debug("", "msg", fmt.Sprint(args...))
	}
}

// Fatalln 模拟 logrus.Fatalln
func Fatalln(args ...any) {
	slog.Error("", "msg", fmt.Sprint(args...))
	os.Exit(1)
}

// Infof 模拟 logrus.Infof
func Infof(format string, args ...any) {
	if level >= INFO {
		slog.Info("", "msg", fmt.Sprintf(format, args...))
	}
}

// Warnf 模拟 logrus.Warnf
func Warnf(format string, args ...any) {
	if level >= WARN {
		slog.Warn("", "msg", fmt.Sprintf(format, args...))
	}
}

// Errorf 模拟 logrus.Errorf
func Errorf(format string, args ...any) {
	if level >= ERROR {
		slog.Error("", "msg", fmt.Sprintf(format, args...))
	}
}

// Debugf 模拟 logrus.Debugf
func Debugf(format string, args ...any) {
	if level >= DEBUG {
		slog.Debug("", "msg", fmt.Sprintf(format, args...))
	}
}

// Fatalf 模拟 logrus.Fatalf
func Fatalf(format string, args ...any) {
	slog.Error("", "msg", fmt.Sprintf(format, args...))
	os.Exit(1)
}

// 保留 proxypool 里的 file logger 占位(proxypool 部分代码引用 FileLogger)
var FileLogger = &fileLoggerStub{}

type fileLoggerStub struct{}

func (f *fileLoggerStub) Warnln(args ...any)  { Warnln(args...) }
func (f *fileLoggerStub) Warnf(format string, args ...any) { Warnf(format, args...) }
func (f *fileLoggerStub) Infoln(args ...any)  { Infoln(args...) }
func (f *fileLoggerStub) Infof(format string, args ...any) { Infof(format, args...) }
