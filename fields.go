package struc

import (
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type Fields []*Field

func (f Fields) SetByteOrder(order binary.ByteOrder) {
	for _, field := range f {
		if field != nil {
			field.Order = order
		}
	}
}

func (f Fields) String() string {
	fields := make([]string, len(f))
	for i, field := range f {
		if field != nil {
			fields[i] = field.String()
		}
	}
	return "{" + strings.Join(fields, ", ") + "}"
}

func (f Fields) Sizeof(val reflect.Value, options *Options) int {
	k := val.Kind()
	for k == reflect.Ptr {
		val = val.Elem()
		k = val.Kind()
		if k == reflect.Invalid { // nil
			return 0
		}
	}
	size := 0
	for i, field := range f {
		if field != nil {
			size += field.Size(val.Field(i), options)
		}
	}
	return size
}

func (f Fields) sizefrom(val reflect.Value, index []int) int {
	field := val.FieldByIndex(index)
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(field.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := int(field.Uint())
		// all the builtin array length types are native int
		// so this guards against weird truncation
		if n < 0 {
			return 0
		}
		return n
	default:
		name := val.Type().FieldByIndex(index).Name
		panic(fmt.Sprintf("sizeof field %T.%s not an integer type", val.Interface(), name))
	}
}

func (f Fields) Pack(buf []byte, val reflect.Value, options *Options) (int, error) {
	for val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if !val.IsValid() { // nil pointer
		return 0, nil
	}
	pos := 0
	for i, field := range f {
		if field == nil {
			continue
		}
		v := val.Field(i)
		length := field.Len
		if field.Sizefrom != nil {
			length = f.sizefrom(val, field.Sizefrom)
		}
		if length <= 0 && field.Slice {
			length = v.Len()
		}
		if field.Sizeof != nil {
			var size int
			fieldToSize := val.FieldByIndex(field.Sizeof)
			switch fieldToSize.Kind() {
			case reflect.Struct, reflect.Ptr:
				if len(field.Sizeof) != 1 {
					return 0, fmt.Errorf("expecting sizeof=1, got %d", len(field.Sizeof))
				}
				size = f[field.Sizeof[0]].Size(fieldToSize, options)
			default:
				size = fieldToSize.Len()
			}
			v = reflect.New(v.Type()).Elem()
			switch field.kind {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				// allocating a new int here has fewer side effects (doesn't update the original struct)
				// but it's a wasteful allocation
				// the old method might work if we just cast the temporary int/uint to the target type
				v.SetInt(int64(size))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				v.SetUint(uint64(size))
			default:
				return 0, fmt.Errorf("sizeof field is not int or uint type: %s, %s", field.Name, v.Type())
			}
		}
		if n, err := field.Pack(buf[pos:], v, length, options); err != nil {
			return n, err
		} else {
			pos += n
		}
	}
	return pos, nil
}

func (f Fields) Unpack(r io.Reader, val reflect.Value, options *Options) error {
	for val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	var tmp [8]byte
	var buf []byte
	for i, field := range f {
		if field == nil {
			continue
		}
		v := val.Field(i)
		length := field.Len
		if field.Sizefrom != nil {
			length = f.sizefrom(val, field.Sizefrom)
		}
		if v.Kind() == reflect.Ptr && !v.Elem().IsValid() {
			if field.Sizefrom == nil || length > 0 { // dont create struct if sizefrom with length=0
				v.Set(reflect.New(v.Type().Elem()))
			}
		}
		if field.Type == Struct {
			if field.Slice {
				vals := v
				if !field.Array {
					vals = reflect.MakeSlice(v.Type(), length, length)
				}
				for i := 0; i < length; i++ {
					v := vals.Index(i)
					fields, err := parseFields(v)
					if err != nil {
						return err
					}
					if err := fields.Unpack(r, v, options); err != nil {
						return err
					}
				}
				if !field.Array {
					v.Set(vals)
				}
			} else {
				if length == 0 {
					continue // dont parse
				}
				// TODO: DRY (we repeat the inner loop above)
				fields, err := parseFields(v)
				if err != nil {
					return err
				}
				if err := fields.Unpack(r, v, options); err != nil {
					return err
				}
			}
			continue
		} else {
			typ := field.Type.Resolve(options)
			if typ == CustomType {
				if err := v.Addr().Interface().(Custom).Unpack(r, length, options); err != nil {
					return err
				}
			} else {
				size := length * field.Type.Resolve(options).Size()
				if size < 8 {
					buf = tmp[:size]
				} else {
					buf = make([]byte, size)
				}
				if _, err := io.ReadFull(r, buf); err != nil {
					return err
				}
				err := field.Unpack(buf[:size], v, length, options)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}
