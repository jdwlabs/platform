package display

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

const (
	colorReset     = "\033[0m"
	colorDim       = "\033[2m"
	colorRed       = "\033[31m"
	colorGreen     = "\033[32m"
	colorYellow    = "\033[33m"
	colorBlue      = "\033[34m"
	colorWhiteOnRd = "\033[37;41m"
)

// NewKVCore returns a zapcore.Core that writes colored key=value lines to w.
func NewKVCore(level zapcore.Level, noColor bool, w io.Writer) zapcore.Core {
	cfg := newConsoleEncoderConfig(noColor)
	enc := &kvEncoder{
		inner:   zapcore.NewConsoleEncoder(cfg),
		noColor: noColor,
	}
	enabler := zap.LevelEnablerFunc(func(l zapcore.Level) bool { return l >= level })
	return zapcore.NewCore(enc, zapcore.AddSync(w), enabler)
}

// kvEncoder wraps a console encoder and renders structured fields as key=value.
// The inner encoder handles time/level/message; we intercept all Add* calls to
// store fields ourselves and render them in EncodeEntry.
type kvEncoder struct {
	inner   zapcore.Encoder
	noColor bool
	fields  []zapcore.Field
}

func (e *kvEncoder) Clone() zapcore.Encoder {
	return &kvEncoder{
		inner:   e.inner.Clone(),
		noColor: e.noColor,
		fields:  append([]zapcore.Field{}, e.fields...),
	}
}

func (e *kvEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	all := append(e.fields, fields...)
	buf, err := e.inner.EncodeEntry(entry, nil)
	if err != nil {
		return buf, err
	}
	if len(all) > 0 {
		data := buf.Bytes()
		if len(data) > 0 && data[len(data)-1] == '\n' {
			buf.TrimNewline()
		}
		for _, f := range all {
			buf.AppendString(" " + f.Key + "=")
			switch f.Type {
			case zapcore.StringType:
				buf.AppendString(f.String)
			case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type:
				buf.AppendString(fmt.Sprintf("%d", f.Integer))
			case zapcore.BoolType:
				if f.Integer == 1 {
					buf.AppendString("true")
				} else {
					buf.AppendString("false")
				}
			case zapcore.Float64Type:
				buf.AppendString(fmt.Sprintf("%g", math.Float64frombits(uint64(f.Integer))))
			case zapcore.Float32Type:
				buf.AppendString(fmt.Sprintf("%g", math.Float32frombits(uint32(f.Integer))))
			case zapcore.DurationType:
				buf.AppendString(time.Duration(f.Integer).String())
			case zapcore.ErrorType:
				if f.Interface != nil {
					buf.AppendString(f.Interface.(error).Error())
				}
			default:
				if f.Interface != nil {
					buf.AppendString(fmt.Sprintf("%v", f.Interface))
				}
			}
		}
		buf.AppendString("\n")
	}
	return buf, nil
}

func (e *kvEncoder) AddArray(key string, v zapcore.ArrayMarshaler) error {
	e.fields = append(e.fields, zap.Array(key, v))
	return nil
}
func (e *kvEncoder) AddObject(key string, v zapcore.ObjectMarshaler) error {
	e.fields = append(e.fields, zap.Object(key, v))
	return nil
}
func (e *kvEncoder) AddBinary(key string, v []byte)     { e.fields = append(e.fields, zap.Binary(key, v)) }
func (e *kvEncoder) AddByteString(key string, v []byte) { e.fields = append(e.fields, zap.ByteString(key, v)) }
func (e *kvEncoder) AddBool(key string, v bool)         { e.fields = append(e.fields, zap.Bool(key, v)) }
func (e *kvEncoder) AddComplex128(key string, v complex128) {
	e.fields = append(e.fields, zap.Complex128(key, v))
}
func (e *kvEncoder) AddComplex64(key string, v complex64) {
	e.fields = append(e.fields, zap.Complex64(key, v))
}
func (e *kvEncoder) AddDuration(key string, v time.Duration) {
	e.fields = append(e.fields, zap.Duration(key, v))
}
func (e *kvEncoder) AddFloat64(key string, v float64) { e.fields = append(e.fields, zap.Float64(key, v)) }
func (e *kvEncoder) AddFloat32(key string, v float32) { e.fields = append(e.fields, zap.Float32(key, v)) }
func (e *kvEncoder) AddInt(key string, v int)         { e.fields = append(e.fields, zap.Int(key, v)) }
func (e *kvEncoder) AddInt64(key string, v int64)     { e.fields = append(e.fields, zap.Int64(key, v)) }
func (e *kvEncoder) AddInt32(key string, v int32)     { e.fields = append(e.fields, zap.Int32(key, v)) }
func (e *kvEncoder) AddInt16(key string, v int16)     { e.fields = append(e.fields, zap.Int16(key, v)) }
func (e *kvEncoder) AddInt8(key string, v int8)       { e.fields = append(e.fields, zap.Int8(key, v)) }
func (e *kvEncoder) AddString(key string, v string)   { e.fields = append(e.fields, zap.String(key, v)) }
func (e *kvEncoder) AddTime(key string, v time.Time)  { e.fields = append(e.fields, zap.Time(key, v)) }
func (e *kvEncoder) AddUint(key string, v uint)       { e.fields = append(e.fields, zap.Uint(key, v)) }
func (e *kvEncoder) AddUint64(key string, v uint64)   { e.fields = append(e.fields, zap.Uint64(key, v)) }
func (e *kvEncoder) AddUint32(key string, v uint32)   { e.fields = append(e.fields, zap.Uint32(key, v)) }
func (e *kvEncoder) AddUint16(key string, v uint16)   { e.fields = append(e.fields, zap.Uint16(key, v)) }
func (e *kvEncoder) AddUint8(key string, v uint8)     { e.fields = append(e.fields, zap.Uint8(key, v)) }
func (e *kvEncoder) AddUintptr(key string, v uintptr) {
	e.fields = append(e.fields, zap.Uintptr(key, v))
}
func (e *kvEncoder) AddReflected(key string, v interface{}) error {
	e.fields = append(e.fields, zap.Any(key, v))
	return nil
}
func (e *kvEncoder) OpenNamespace(key string) {
	e.fields = append(e.fields, zap.Namespace(key))
}

func newConsoleEncoderConfig(noColor bool) zapcore.EncoderConfig {
	cfg := zapcore.EncoderConfig{
		TimeKey:          "ts",
		LevelKey:         "level",
		MessageKey:       "msg",
		ConsoleSeparator: " ",
		EncodeDuration:   zapcore.StringDurationEncoder,
	}
	if noColor {
		cfg.EncodeTime = plainTimeEncoder
		cfg.EncodeLevel = paddedLevelEncoder
	} else {
		cfg.EncodeTime = dimTimeEncoder
		cfg.EncodeLevel = colorLevelEncoder
	}
	return cfg
}

func plainTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("15:04:05"))
}

func dimTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(colorDim + t.Format("15:04:05") + colorReset)
}

func paddedLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("%-5s", l.CapitalString()))
}

func colorLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	var color string
	switch l {
	case zapcore.FatalLevel:
		color = colorWhiteOnRd
	case zapcore.ErrorLevel:
		color = colorRed
	case zapcore.WarnLevel:
		color = colorYellow
	case zapcore.DebugLevel:
		color = colorBlue
	default:
		color = colorGreen
	}
	enc.AppendString(color + fmt.Sprintf("%-5s", l.CapitalString()) + colorReset)
}

func parseZapLevel(s string) zapcore.Level {
	switch strings.ToLower(s) {
	case "debug", "trace":
		return zap.DebugLevel
	case "warn", "warning":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}
