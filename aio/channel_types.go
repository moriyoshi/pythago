package aio

import (
	"fmt"
	"math"
	"math/big"

	"github.com/moriyoshi/pythago/py"
)

type channelSignedType interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

type channelUnsignedType interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

func channelExact[T any](v any) (T, error) {
	if value, ok := v.(T); ok {
		return value, nil
	}
	var zero T
	return zero, fmt.Errorf("aio: cannot send %T to a channel of %T", v, zero)
}

func channelAny(v any) (any, error) { return v, nil }

func channelSigned[T channelSignedType](v any) (T, error) {
	var zero T
	n, ok := v.(int64)
	if nBig, isBig := v.(*big.Int); isBig && nBig.IsInt64() {
		n, ok = nBig.Int64(), true
	}
	if ok {
		value := T(n)
		if int64(value) == n {
			return value, nil
		}
	}
	return zero, fmt.Errorf("aio: %v does not fit channel element %T", v, zero)
}

func channelUnsigned[T channelUnsignedType](v any) (T, error) {
	var zero T
	var n uint64
	valid := false
	switch v := v.(type) {
	case int64:
		if v >= 0 {
			n, valid = uint64(v), true
		}
	case *big.Int:
		if v.IsUint64() {
			n, valid = v.Uint64(), true
		}
	}
	if valid {
		value := T(n)
		if uint64(value) == n {
			return value, nil
		}
	}
	return zero, fmt.Errorf("aio: %v does not fit channel element %T", v, zero)
}

func channelFloat[T ~float32 | ~float64](v any) (T, error) {
	var zero T
	var n float64
	switch v := v.(type) {
	case float64:
		n = v
	case int64:
		n = float64(v)
	default:
		return zero, fmt.Errorf("aio: cannot send %T to a channel of %T", v, zero)
	}
	value := T(n)
	if math.IsInf(float64(value), 0) && !math.IsInf(n, 0) {
		return zero, fmt.Errorf("aio: %v does not fit channel element %T", v, zero)
	}
	return value, nil
}

func channelSlice[T any](decode func(any) (T, error)) func(any) ([]T, error) {
	return func(v any) ([]T, error) {
		if v == nil {
			return nil, nil
		}
		if value, ok := v.([]T); ok {
			return value, nil
		}
		items, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("aio: cannot send %T to a slice channel", v)
		}
		value := make([]T, len(items))
		for i, item := range items {
			var err error
			value[i], err = decode(item)
			if err != nil {
				return nil, err
			}
		}
		return value, nil
	}
}

func channelMap[K comparable, V any](key func(any) (K, error), item func(any) (V, error)) func(any) (map[K]V, error) {
	return func(v any) (map[K]V, error) {
		if v == nil {
			return nil, nil
		}
		if value, ok := v.(map[K]V); ok {
			return value, nil
		}
		items, ok := v.(map[any]any)
		if !ok {
			return nil, fmt.Errorf("aio: cannot send %T to a map channel", v)
		}
		value := make(map[K]V, len(items))
		for k, v := range items {
			k2, err := key(k)
			if err != nil {
				return nil, err
			}
			v2, err := item(v)
			if err != nil {
				return nil, err
			}
			value[k2] = v2
		}
		return value, nil
	}
}

// Python's None travels as a nil lease; anything else must be a str or a buffer exporter.
func stringFromObject(o *py.Object) (*py.String, error) {
	if o.IsNone() {
		return nil, nil
	}
	return o.Unicode()
}

func bufferFromObject(o *py.Object) (*py.OwnedBuffer, error) {
	if o.IsNone() {
		return nil, nil
	}
	return o.OwnedBuffer()
}

// Concrete type variants keep channel detection and operations free of reflection.
func builtinChannel(v any) (channelAdapter, bool) {
	switch v.(type) {
	case chan *py.String, <-chan *py.String, chan<- *py.String:
		adapter, ok := channelType[*py.String](v, nil, nil)
		adapter.(*typedChannel[*py.String]).objectDecode = stringFromObject
		return adapter, ok
	case chan *py.OwnedBuffer, <-chan *py.OwnedBuffer, chan<- *py.OwnedBuffer:
		adapter, ok := channelType[*py.OwnedBuffer](v, nil, nil)
		adapter.(*typedChannel[*py.OwnedBuffer]).objectDecode = bufferFromObject
		return adapter, ok
	case chan bool, <-chan bool, chan<- bool:
		return channelType[bool](v, channelExact[bool], nil)
	case chan string, <-chan string, chan<- string:
		return channelType[string](v, channelExact[string], nil)
	case chan any, <-chan any, chan<- any:
		return channelType[any](v, channelAny, nil)
	case chan int, <-chan int, chan<- int:
		return channelType[int](v, channelSigned[int], nil)
	case chan int8, <-chan int8, chan<- int8:
		return channelType[int8](v, channelSigned[int8], nil)
	case chan int16, <-chan int16, chan<- int16:
		return channelType[int16](v, channelSigned[int16], nil)
	case chan int32, <-chan int32, chan<- int32:
		return channelType[int32](v, channelSigned[int32], nil)
	case chan int64, <-chan int64, chan<- int64:
		return channelType[int64](v, channelSigned[int64], nil)
	case chan uint, <-chan uint, chan<- uint:
		return channelType[uint](v, channelUnsigned[uint], nil)
	case chan uint8, <-chan uint8, chan<- uint8:
		return channelType[uint8](v, channelUnsigned[uint8], nil)
	case chan uint16, <-chan uint16, chan<- uint16:
		return channelType[uint16](v, channelUnsigned[uint16], nil)
	case chan uint32, <-chan uint32, chan<- uint32:
		return channelType[uint32](v, channelUnsigned[uint32], nil)
	case chan uint64, <-chan uint64, chan<- uint64:
		return channelType[uint64](v, channelUnsigned[uint64], nil)
	case chan uintptr, <-chan uintptr, chan<- uintptr:
		return channelType[uintptr](v, channelUnsigned[uintptr], nil)
	case chan float32, <-chan float32, chan<- float32:
		return channelType[float32](v, channelFloat[float32], nil)
	case chan float64, <-chan float64, chan<- float64:
		return channelType[float64](v, channelFloat[float64], nil)
	case chan []byte, <-chan []byte, chan<- []byte:
		return channelType[[]byte](v, channelSlice[byte](channelUnsigned[byte]), nil)
	case chan []int, <-chan []int, chan<- []int:
		return channelType[[]int](v, channelSlice[int](channelSigned[int]), nil)
	case chan []string, <-chan []string, chan<- []string:
		return channelType[[]string](v, channelSlice[string](channelExact[string]), nil)
	case chan []any, <-chan []any, chan<- []any:
		return channelType[[]any](v, channelSlice[any](channelAny), nil)
	case chan map[string]string, <-chan map[string]string, chan<- map[string]string:
		return channelType[map[string]string](v, channelMap[string, string](channelExact[string], channelExact[string]), nil)
	case chan map[string]any, <-chan map[string]any, chan<- map[string]any:
		return channelType[map[string]any](v, channelMap[string, any](channelExact[string], channelAny), nil)
	case chan map[any]any, <-chan map[any]any, chan<- map[any]any:
		return channelType[map[any]any](v, channelMap[any, any](channelAny, channelAny), nil)
	default:
		return nil, false
	}
}
