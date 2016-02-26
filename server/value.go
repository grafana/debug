// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package server

import (
	"fmt"

	"golang.org/x/debug"
	"golang.org/x/debug/dwarf"
)

// value peeks the program's memory at the given address, parsing it as a value of type t.
func (s *Server) value(t dwarf.Type, addr uint64) (debug.Value, error) {
	// readBasic reads the memory for a basic type of size n bytes.
	readBasic := func(n int64) ([]byte, error) {
		switch n {
		case 1, 2, 4, 8, 16:
		default:
			return nil, fmt.Errorf("invalid size: %d", n)
		}
		buf := make([]byte, n)
		if err := s.peek(uintptr(addr), buf); err != nil {
			return nil, err
		}
		return buf, nil
	}

	switch t := t.(type) {
	case *dwarf.CharType, *dwarf.IntType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading integer: %s", err)
		}
		x := s.arch.IntN(buf)
		switch bs {
		case 1:
			return int8(x), nil
		case 2:
			return int16(x), nil
		case 4:
			return int32(x), nil
		case 8:
			return int64(x), nil
		default:
			return nil, fmt.Errorf("invalid integer size: %d", bs)
		}
	case *dwarf.UcharType, *dwarf.UintType, *dwarf.AddrType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading unsigned integer: %s", err)
		}
		x := s.arch.UintN(buf)
		switch bs {
		case 1:
			return uint8(x), nil
		case 2:
			return uint16(x), nil
		case 4:
			return uint32(x), nil
		case 8:
			return uint64(x), nil
		default:
			return nil, fmt.Errorf("invalid unsigned integer size: %d", bs)
		}
	case *dwarf.BoolType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading boolean: %s", err)
		}
		for _, b := range buf {
			if b != 0 {
				return true, nil
			}
		}
		return false, nil
	case *dwarf.FloatType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading float: %s", err)
		}
		switch bs {
		case 4:
			return s.arch.Float32(buf), nil
		case 8:
			return s.arch.Float64(buf), nil
		default:
			return nil, fmt.Errorf("invalid float size: %d", bs)
		}
	case *dwarf.ComplexType:
		bs := t.Common().ByteSize
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading complex: %s", err)
		}
		switch bs {
		case 8:
			return s.arch.Complex64(buf), nil
		case 16:
			return s.arch.Complex128(buf), nil
		default:
			return nil, fmt.Errorf("invalid complex size: %d", bs)
		}
	case *dwarf.PtrType:
		bs := t.Common().ByteSize
		if bs != int64(s.arch.PointerSize) {
			return nil, fmt.Errorf("invalid pointer size: %d", bs)
		}
		buf, err := readBasic(bs)
		if err != nil {
			return nil, fmt.Errorf("reading pointer: %s", err)
		}
		return debug.Pointer{
			TypeID:  uint64(t.Type.Common().Offset),
			Address: uint64(s.arch.Uintptr(buf)),
		}, nil
	case *dwarf.SliceType:
		ptr, err := s.peekPtrStructField(&t.StructType, addr, "array")
		if err != nil {
			return nil, fmt.Errorf("reading slice location: %s", err)
		}
		length, err := s.peekUintOrIntStructField(&t.StructType, addr, "len")
		if err != nil {
			return nil, fmt.Errorf("reading slice length: %s", err)
		}
		capacity, err := s.peekUintOrIntStructField(&t.StructType, addr, "cap")
		if err != nil {
			return nil, fmt.Errorf("reading slice capacity: %s", err)
		}
		if capacity < length {
			return nil, fmt.Errorf("slice's capacity %d is less than its length %d", capacity, length)
		}

		return debug.Slice{
			debug.Array{
				ElementTypeID: uint64(t.ElemType.Common().Offset),
				Address:       uint64(ptr),
				Length:        length,
				StrideBits:    uint64(t.ElemType.Common().ByteSize) * 8,
			},
			capacity,
		}, nil
	case *dwarf.ArrayType:
		length := t.Count
		stride := t.StrideBitSize
		if stride%8 != 0 {
			return nil, fmt.Errorf("array is not byte-aligned")
		}
		return debug.Array{
			ElementTypeID: uint64(t.Type.Common().Offset),
			Address:       uint64(addr),
			Length:        uint64(length),
			StrideBits:    uint64(stride),
		}, nil
	case *dwarf.StructType:
		fields := make([]debug.StructField, len(t.Field))
		for i, field := range t.Field {
			fields[i] = debug.StructField{
				Name: field.Name,
				Var: debug.Var{
					TypeID:  uint64(field.Type.Common().Offset),
					Address: uint64(addr) + uint64(field.ByteOffset),
				},
			}
		}
		return debug.Struct{fields}, nil
	case *dwarf.TypedefType:
		return s.value(t.Type, addr)
	case *dwarf.MapType:
		length, err := s.peekMapLength(t, addr)
		if err != nil {
			return nil, err
		}
		return debug.Map{
			TypeID:  uint64(t.Common().Offset),
			Address: addr,
			Length:  length,
		}, nil
	case *dwarf.StringType:
		ptr, err := s.peekPtrStructField(&t.StructType, addr, "str")
		if err != nil {
			return nil, fmt.Errorf("reading string location: %s", err)
		}
		length, err := s.peekUintOrIntStructField(&t.StructType, addr, "len")
		if err != nil {
			return nil, fmt.Errorf("reading string length: %s", err)
		}

		const maxStringSize = 256

		n := length
		if n > maxStringSize {
			n = maxStringSize
		}
		tmp := make([]byte, n)
		if err := s.peekBytes(ptr, tmp); err != nil {
			return nil, fmt.Errorf("reading string contents: %s", err)
		}
		return debug.String{Length: length, String: string(tmp)}, nil
	case *dwarf.ChanType:
		pt, ok := t.TypedefType.Type.(*dwarf.PtrType)
		if !ok {
			return nil, fmt.Errorf("reading channel: type is not a pointer")
		}
		st, ok := pt.Type.(*dwarf.StructType)
		if !ok {
			return nil, fmt.Errorf("reading channel: type is not a pointer to struct")
		}

		a, err := s.peekPtr(addr)
		if err != nil {
			return nil, fmt.Errorf("reading channel pointer: %s", err)
		}
		if a == 0 {
			// This channel is nil.
			return debug.Channel{
				ElementTypeID: uint64(t.ElemType.Common().Offset),
				Address:       0,
				Buffer:        0,
				Length:        0,
				Capacity:      0,
				Stride:        uint64(t.ElemType.Common().ByteSize),
				BufferStart:   0,
			}, nil
		}

		buf, err := s.peekPtrStructField(st, a, "buf")
		if err != nil {
			return nil, fmt.Errorf("reading channel buffer location: %s", err)
		}
		qcount, err := s.peekUintOrIntStructField(st, a, "qcount")
		if err != nil {
			return nil, fmt.Errorf("reading channel length: %s", err)
		}
		capacity, err := s.peekUintOrIntStructField(st, a, "dataqsiz")
		if err != nil {
			return nil, fmt.Errorf("reading channel capacity: %s", err)
		}
		recvx, err := s.peekUintOrIntStructField(st, a, "recvx")
		if err != nil {
			return nil, fmt.Errorf("reading channel buffer index: %s", err)
		}
		return debug.Channel{
			ElementTypeID: uint64(t.ElemType.Common().Offset),
			Address:       a,
			Buffer:        buf,
			Length:        qcount,
			Capacity:      capacity,
			Stride:        uint64(t.ElemType.Common().ByteSize),
			BufferStart:   recvx,
		}, nil
	case *dwarf.FuncType:
		a, err := s.peekPtr(addr)
		if err != nil {
			return nil, fmt.Errorf("reading func: %s", err)
		}
		return debug.Func{Address: a}, nil
	case *dwarf.InterfaceType:
		return debug.Interface{}, nil
		// TODO: more types
	}
	return nil, fmt.Errorf("Unsupported type %T", t)
}